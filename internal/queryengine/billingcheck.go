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
// with a value; plus the totals of all metering points of the community. Evaluated on the raw rows of
// energy_data (the aggregates keep only MIN(qov), which hides L3). A cell counts only if a row exists
// for it, so a missing value is L0 as in v1 (the wide-line loader presets missing cells with 1).
// Sums take one value per OBIS block (T/R variants: last write wins, as in the loader); for the
// quality any row of the block counts, so an L3 in one variant is never hidden by another.
// Known difference to v1: on the autumn clock change v2 keeps both 02:00-02:45 hours, v1 folds them
// into one wall-clock row.

const (
	billingCheckMaxPeriod = 367 * 24 * time.Hour // a billing period is at most one year
	billingCheckMaxCps    = 10000
	BillingCheckMaxBody   = 4 << 20 // 10,000 entries take about 1.5 MB

	consumerDirection = "CONSUMPTION"
	producerDirection = "GENERATION"
)

// billingCheckMaxEnd keeps the period inside the dates Postgres and v1 can represent.
var billingCheckMaxEnd = time.Date(2999, 12, 31, 0, 0, 0, 0, time.UTC).UnixMilli()

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
	if req.Start <= 0 || req.End < req.Start || req.End > billingCheckMaxEnd {
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

// bcCell is one slot of a metering point at one timestamp.
type bcCell struct {
	set      bool
	value    float64 // last row of the block
	hasValue bool    // a row of the block with quality not L0
	hasL3    bool    // a row of the block with L3
}

// bcEntry is a requested entry resolved against the counterpoints.
type bcEntry struct {
	idx      int // index in the request and the result
	from, to time.Time
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
	if len(cps) == 0 {
		return res, nil
	}
	// Every counterpoint gets a position; the cells of the current timestamp are kept per position.
	pos := make(map[string]int, len(cps))
	for i, cp := range cps {
		pos[cp.MeteringPoint] = i
	}
	// Requested entries per position; an entry whose direction differs from the stored one never
	// matches (v1 skips it as well).
	entries := make([][]bcEntry, len(cps))
	for i, cp := range req.Cps {
		p, ok := pos[cp.MeteringPoint]
		if !ok || (cps[p].Direction == counterpoint.DirectionProducer) != (cp.Direction == producerDirection) {
			continue
		}
		to := time.Date(2999, 12, 31, 23, 59, 59, 0, time.Local)
		if cp.InactiveSince > 0 { // no end date: active without end
			d := time.UnixMilli(cp.InactiveSince)
			to = time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, time.Local) // end of the inactive day
		}
		entries[p] = append(entries[p], bcEntry{idx: i, from: time.UnixMilli(cp.ActiveSince), to: to})
	}

	rows, err := e.pool.Query(ctx, loadAllSlotsSQL, tenant, ec, periodStart, periodEnd)
	if err != nil {
		return nil, fmt.Errorf("queryengine: billing check: %w", err)
	}
	defer rows.Close()

	var (
		cells     = make([][3]bcCell, len(cps))
		touched   []int // positions with cells at the current timestamp, in row order
		currentTS time.Time
	)
	flush := func() {
		for _, p := range touched {
			res.addSlot(cps[p].Direction, &cells[p], currentTS, entries[p])
			cells[p] = [3]bcCell{}
		}
		touched = touched[:0]
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
		p, ok := pos[mp]
		if !ok {
			continue
		}
		slot, ok := consumerSlotForCode(code)
		if cps[p].Direction == counterpoint.DirectionProducer {
			slot, ok = producerSlotForCode(code)
		}
		if !ok {
			continue
		}
		c := &cells[p]
		if !c[0].set && !c[1].set && !c[2].set {
			touched = append(touched, p)
		}
		c[slot].set = true
		c[slot].value = value
		c[slot].hasValue = c[slot].hasValue || qov != 0
		c[slot].hasL3 = c[slot].hasL3 || qov == 3
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("queryengine: billing check rows: %w", err)
	}
	flush()
	return res, nil
}

func (res *BillingCheckResponse) addSlot(dir counterpoint.Direction, cells *[3]bcCell, ts time.Time, entries []bcEntry) {
	t := &res.Totals
	if dir == counterpoint.DirectionProducer {
		t.Produced += cells[0].value
		t.Surplus += cells[1].value
	} else {
		t.Consumed += cells[0].value
		t.Allocated += cells[1].value
		t.SelfCovered += cells[2].value
	}

	hasValue, hasL3 := false, false
	for _, c := range cells {
		hasValue = hasValue || c.hasValue
		hasL3 = hasL3 || c.hasL3
	}
	for _, e := range entries {
		if ts.Before(e.from) || ts.After(e.to) {
			continue
		}
		m := &res.Meters[e.idx]
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
}
