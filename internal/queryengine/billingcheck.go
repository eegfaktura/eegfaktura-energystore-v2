package queryengine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
)

// Checks before a billing run (platform#148), same request and response as v1
// POST /eeg/v2/{ecid}/billing-check. Per requested entry: L3 quarter hours, first/last quarter hour
// with a value; plus community totals. Evaluated on the raw rows of energy_data (the aggregates keep
// only MIN(qov), which hides L3). Like the v1 wide lines, one slot per OBIS block (T/R variants:
// last write wins); unlike the wide-line loader, a cell counts only if a row exists for it, so a
// missing value is L0 as in v1.

const (
	billingCheckMaxPeriod = 367 * 24 * time.Hour // a billing period is at most one year
	billingCheckMaxCps    = 10000

	consumerDirection = "CONSUMPTION"
	producerDirection = "GENERATION"
)

type BillingCheckRequest struct {
	Start int64            `json:"start"`
	End   int64            `json:"end"`
	Cps   []BillingCheckCp `json:"cps"`
}

type BillingCheckCp struct {
	MeteringPoint string `json:"meteringPoint"`
	Direction     string `json:"direction"`
	ActiveSince   int64  `json:"activeSince"`
	InactiveSince int64  `json:"inactiveSince"`
}

type BillingCheckMeter struct {
	MeteringPoint string   `json:"meteringPoint"`
	Direction     string   `json:"direction"`
	ActiveSince   int64    `json:"activeSince"`
	InactiveSince int64    `json:"inactiveSince"`
	L3Count       int      `json:"l3Count"`
	L3Days        []string `json:"l3Days"`
	FirstData     *int64   `json:"firstData"`
	LastData      *int64   `json:"lastData"`
}

type BillingCheckTotals struct {
	Produced    float64 `json:"produced"`    // producer G.01
	Consumed    float64 `json:"consumed"`    // consumer G.01
	Allocated   float64 `json:"allocated"`   // consumer G.02
	SelfCovered float64 `json:"selfCovered"` // consumer G.03
	Surplus     float64 `json:"surplus"`     // producer P.01
}

type BillingCheckResponse struct {
	PeriodStart int64               `json:"periodStart"`
	PeriodEnd   int64               `json:"periodEnd"`
	Meters      []BillingCheckMeter `json:"meters"`
	Totals      BillingCheckTotals  `json:"totals"`
}

// Validate rejects requests that cannot be a billing period of one community (same rules as v1).
func (req *BillingCheckRequest) Validate() error {
	if req.Start <= 0 || req.End < req.Start {
		return errors.New("invalid period")
	}
	if req.End-req.Start > billingCheckMaxPeriod.Milliseconds() {
		return errors.New("period longer than one year")
	}
	if len(req.Cps) > billingCheckMaxCps {
		return errors.New("too many metering points")
	}
	for _, cp := range req.Cps {
		if cp.MeteringPoint == "" {
			return errors.New("metering point missing")
		}
		if cp.Direction != consumerDirection && cp.Direction != producerDirection {
			return errors.New("invalid direction")
		}
	}
	return nil
}

var noEndDate = time.Date(2999, 12, 31, 0, 0, 0, 0, time.UTC).UnixMilli()

// lastMinuteOfDay: v1 IsLineDateOutOfRange treats inactiveSince as the whole day (until 23:59).
const lastMinuteOfDay = 86340000

type bcCell struct {
	set   bool
	value float64
	qov   int
}

// QueryBillingCheck evaluates the checks for (tenant, ec) and the requested entries.
func (e *Engine) QueryBillingCheck(ctx context.Context, tenant, ec string, req *BillingCheckRequest) (*BillingCheckResponse, error) {
	start, end := time.UnixMilli(req.Start), time.UnixMilli(req.End)
	periodStart := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local)
	periodEnd := time.Date(end.Year(), end.Month(), end.Day(), 23, 45, 0, 0, time.Local)

	res := &BillingCheckResponse{
		PeriodStart: periodStart.UnixMilli(),
		PeriodEnd:   periodEnd.UnixMilli(),
		Meters:      make([]BillingCheckMeter, len(req.Cps)),
	}
	for i, cp := range req.Cps {
		res.Meters[i] = BillingCheckMeter{MeteringPoint: cp.MeteringPoint, Direction: cp.Direction,
			ActiveSince: cp.ActiveSince, InactiveSince: cp.InactiveSince, L3Days: []string{}}
	}

	cps, err := e.repo.ListByEC(ctx, tenant, ec)
	if err != nil {
		return nil, err
	}
	direction := make(map[string]counterpoint.Direction, len(cps))
	for _, cp := range cps {
		direction[cp.MeteringPoint] = cp.Direction
	}

	// Entries per metering point whose direction matches the stored one; others never match (v1 skips
	// them as well).
	entries := map[string][]int{}
	for i, cp := range req.Cps {
		want := counterpoint.DirectionConsumer
		if cp.Direction == producerDirection {
			want = counterpoint.DirectionProducer
		}
		if d, ok := direction[cp.MeteringPoint]; ok && d == want {
			entries[cp.MeteringPoint] = append(entries[cp.MeteringPoint], i)
		}
	}
	if len(entries) == 0 {
		return res, nil
	}

	rows, err := e.pool.Query(ctx, loadAllSlotsSQL, tenant, ec, periodStart, periodEnd)
	if err != nil {
		return nil, fmt.Errorf("queryengine: billing check: %w", err)
	}
	defer rows.Close()

	// The cells of the current timestamp per entry: 3 slots (consumer) or 2 (producer).
	var (
		current   = map[int]*[3]bcCell{}
		currentTS time.Time
	)
	flush := func() {
		for i, cells := range current {
			res.addSlot(i, req.Cps[i].Direction, currentTS, cells)
		}
		current = map[int]*[3]bcCell{}
	}

	for rows.Next() {
		var (
			ts    time.Time
			mp    string
			code  string
			value float64
			qov   int16
		)
		if err := rows.Scan(&ts, &mp, &code, &value, &qov); err != nil {
			return nil, fmt.Errorf("queryengine: billing check scan: %w", err)
		}
		if !ts.Equal(currentTS) {
			flush()
			currentTS = ts
		}
		for _, i := range entries[mp] {
			cp := req.Cps[i]
			inactiveSince := cp.InactiveSince
			if inactiveSince <= 0 { // no end date: active without end
				inactiveSince = noEndDate
			}
			if ts.Before(time.UnixMilli(cp.ActiveSince)) || ts.After(time.UnixMilli(inactiveSince+lastMinuteOfDay)) {
				continue
			}
			slot, ok := consumerSlotForCode(code)
			if cp.Direction == producerDirection {
				slot, ok = producerSlotForCode(code)
			}
			if !ok {
				continue
			}
			cells := current[i]
			if cells == nil {
				cells = &[3]bcCell{}
				current[i] = cells
			}
			cells[slot] = bcCell{set: true, value: value, qov: int(qov)}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("queryengine: billing check rows: %w", err)
	}
	flush()
	return res, nil
}

func (res *BillingCheckResponse) addSlot(i int, dir string, ts time.Time, cells *[3]bcCell) {
	t := &res.Totals
	if dir == consumerDirection {
		t.Consumed += cells[0].value
		t.Allocated += cells[1].value
		t.SelfCovered += cells[2].value
	} else {
		t.Produced += cells[0].value
		t.Surplus += cells[1].value
	}

	hasValue, hasL3 := false, false
	for _, c := range cells {
		hasValue = hasValue || (c.set && c.qov != 0)
		hasL3 = hasL3 || (c.set && c.qov == 3)
	}
	m := &res.Meters[i]
	if hasValue {
		ms := ts.UnixMilli()
		if m.FirstData == nil {
			m.FirstData = &ms
		}
		m.LastData = &ms
	}
	if hasL3 {
		m.L3Count += 1
		day := ts.In(time.Local).Format("2006-01-02")
		if len(m.L3Days) == 0 || m.L3Days[len(m.L3Days)-1] != day {
			m.L3Days = append(m.L3Days, day)
		}
	}
}
