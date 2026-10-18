package queryengine

import (
	"context"
	"testing"
	"time"

	_ "time/tzdata"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
)

// Same cases as v1 store/billingcheck_function_test.go (platform#148), so both stores answer alike.

const (
	bcCons  = "AT0030000000000000000000000000001"
	bcCons2 = "AT0030000000000000000000000000002"
	bcProd  = "AT0030000000000000000000000000003"
)

func bcDay(d int) time.Time { return time.Date(2026, 2, d, 0, 0, 0, 0, time.Local) }

func bcAt(d, h, m int) time.Time { return time.Date(2026, 2, d, h, m, 0, 0, time.Local) }

func bcMeta(mock pgxmock.PgxPoolIface) {
	now := time.Now()
	expectMeta(mock, metaRows(mock).
		AddRow("vfeeg", "TE100200", bcCons, int16(counterpoint.DirectionConsumer), 0,
			(*time.Time)(nil), (*time.Time)(nil), []byte(`{}`), now).
		AddRow("vfeeg", "TE100200", bcCons2, int16(counterpoint.DirectionConsumer), 1,
			(*time.Time)(nil), (*time.Time)(nil), []byte(`{}`), now).
		AddRow("vfeeg", "TE100200", bcProd, int16(counterpoint.DirectionProducer), 0,
			(*time.Time)(nil), (*time.Time)(nil), []byte(`{}`), now))
}

func bcRequest(cps ...BillingCheckCp) *BillingCheckRequest {
	return &BillingCheckRequest{Start: bcDay(1).UnixMilli(), End: bcDay(28).UnixMilli(), Cps: cps}
}

func bcConsumer(mp string, active, inactive int64) BillingCheckCp {
	return BillingCheckCp{MeteringPoint: mp, Direction: consumerDirection, ActiveSince: active, InactiveSince: inactive}
}

func expectBillingSlots(mock pgxmock.PgxPoolIface, rows *pgxmock.Rows) {
	expectSlots(mock, bcDay(1), time.Date(2026, 2, 28, 23, 45, 0, 0, time.Local), rows)
}

func TestBillingCheck_L3(t *testing.T) {
	eng, mock := newMockEngine(t)
	defer mock.Close()
	bcMeta(mock)

	expectBillingSlots(mock, slotRows(mock).
		// L3 of the second consumer before its activation on 05.02.
		AddRow(bcAt(3, 10, 0), bcCons2, "1-1:1.9.0 G.01", 1.0, int16(3)).
		// one L3 value
		AddRow(bcAt(10, 8, 0), bcCons, "1-1:1.9.0 G.01", 1.0, int16(1)).
		AddRow(bcAt(10, 8, 0), bcCons, "1-1:2.9.0 G.02", 0.5, int16(3)).
		// two L3 values in the same quarter hour count once
		AddRow(bcAt(10, 8, 15), bcCons, "1-1:1.9.0 G.01", 1.0, int16(3)).
		AddRow(bcAt(10, 8, 15), bcCons, "1-1:2.9.0 G.03", 0.4, int16(3)).
		// L2 is no warning
		AddRow(bcAt(11, 9, 0), bcCons, "1-1:1.9.0 G.01", 1.0, int16(2)).
		// L3 of the producer
		AddRow(bcAt(12, 10, 0), bcProd, "1-1:2.9.0 P.01", 0.3, int16(3)).
		// an L3 in one code variant is not hidden by another; the value is taken once
		AddRow(bcAt(13, 10, 0), bcCons, "1-1:1.9.0 G.01", 1.0, int16(3)).
		AddRow(bcAt(13, 10, 0), bcCons, "1-1:1.9.0 G.01T", 1.0, int16(1)))

	res, err := eng.QueryBillingCheck(context.Background(), "vfeeg", "TE100200", bcRequest(
		bcConsumer(bcCons, 0, 0),
		bcConsumer(bcCons2, bcDay(5).UnixMilli(), 0),
		BillingCheckCp{MeteringPoint: bcProd, Direction: producerDirection},
	))
	if err != nil {
		t.Fatal(err)
	}
	if m := res.Meters[0]; m.L3Count != 3 || len(m.L3Days) != 2 || m.L3Days[0] != "2026-02-10" || m.L3Days[1] != "2026-02-13" {
		t.Errorf("consumer: got %d %v, want 3 [2026-02-10 2026-02-13]", m.L3Count, m.L3Days)
	}
	if m := res.Meters[1]; m.L3Count != 0 || len(m.L3Days) != 0 {
		t.Errorf("L3 before activation counted: %d %v", m.L3Count, m.L3Days)
	}
	if m := res.Meters[2]; m.L3Count != 1 || m.L3Days[0] != "2026-02-12" {
		t.Errorf("producer: got %d %v", m.L3Count, m.L3Days)
	}
	// all metering points count, also the second consumer before its activation; the T variant
	// replaces the plain code
	if res.Totals.Consumed != 5.0 {
		t.Errorf("consumed %v, want 5", res.Totals.Consumed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestBillingCheck_CoverageAndTotals(t *testing.T) {
	eng, mock := newMockEngine(t)
	defer mock.Close()
	bcMeta(mock)

	first, last := bcAt(1, 0, 0), bcAt(28, 23, 45)
	expectBillingSlots(mock, slotRows(mock).
		AddRow(first, bcCons, "1-1:1.9.0 G.01", 1.0, int16(1)).
		AddRow(first, bcCons, "1-1:2.9.0 G.02", 0.5, int16(1)).
		AddRow(first, bcCons, "1-1:2.9.0 G.03", 0.4, int16(1)).
		AddRow(first, bcProd, "1-1:2.9.0 G.01", 2.0, int16(1)).
		AddRow(first, bcProd, "1-1:2.9.0 P.01", 0.3, int16(1)).
		// second consumer: from its activation until 20.02.; an L0 row is no value
		AddRow(bcAt(5, 0, 0), bcCons2, "1-1:1.9.0 G.01", 1.0, int16(1)).
		AddRow(bcAt(9, 23, 45), bcCons, "1-1:1.9.0 G.01", 1.0, int16(1)).
		AddRow(bcAt(20, 23, 45), bcCons2, "1-1:1.9.0 G.01", 1.0, int16(1)).
		AddRow(bcAt(21, 0, 0), bcCons2, "1-1:1.9.0 G.01", 0.0, int16(0)).
		AddRow(last, bcCons, "1-1:1.9.0 G.01", 1.0, int16(1)))

	res, err := eng.QueryBillingCheck(context.Background(), "vfeeg", "TE100200", bcRequest(
		bcConsumer(bcCons, 0, 0),
		bcConsumer(bcCons2, bcDay(5).UnixMilli(), 0),
		BillingCheckCp{MeteringPoint: bcProd, Direction: producerDirection},
		bcConsumer("AT0030000000000000000000000000099", 0, 0),               // unknown metering point
		bcConsumer(bcCons, 0, bcDay(9).UnixMilli()),                         // same metering point, earlier owner
		BillingCheckCp{MeteringPoint: bcCons, Direction: producerDirection}, // wrong direction
	))
	if err != nil {
		t.Fatal(err)
	}

	if res.PeriodStart != first.UnixMilli() || res.PeriodEnd != last.UnixMilli() {
		t.Errorf("period %d..%d", res.PeriodStart, res.PeriodEnd)
	}
	want := []struct{ first, last *time.Time }{
		{&first, &last},
		{ptr(bcAt(5, 0, 0)), ptr(bcAt(20, 23, 45))},
		{&first, &first},
		{nil, nil},
		{&first, ptr(bcAt(9, 23, 45))},
		{nil, nil},
	}
	for i, w := range want {
		m := res.Meters[i]
		if !sameTime(m.FirstData, w.first) || !sameTime(m.LastData, w.last) {
			t.Errorf("entry %d: first %v last %v", i, m.FirstData, m.LastData)
		}
	}
	if res.Meters[4].InactiveSince != bcDay(9).UnixMilli() {
		t.Error("request values are echoed")
	}
	// totals over all rows of the community, not per requested entry: the earlier owner does not
	// double them
	tot := res.Totals
	if tot.Consumed != 5.0 || tot.Allocated != 0.5 || tot.SelfCovered != 0.4 || tot.Produced != 2.0 || tot.Surplus != 0.3 {
		t.Errorf("totals %+v", tot)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestBillingCheck_UnknownMeteringPointKeepsCommunityTotals(t *testing.T) {
	eng, mock := newMockEngine(t)
	defer mock.Close()
	bcMeta(mock)
	expectBillingSlots(mock, slotRows(mock).
		AddRow(bcAt(2, 0, 0), bcCons, "1-1:1.9.0 G.01", 1.0, int16(1)))

	res, err := eng.QueryBillingCheck(context.Background(), "vfeeg", "TE100200",
		bcRequest(bcConsumer("AT0030000000000000000000000000099", 0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Meters) != 1 || res.Meters[0].FirstData != nil || res.Totals.Consumed != 1.0 {
		t.Errorf("unexpected %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestBillingCheckRequestValidate(t *testing.T) {
	start, endOfYear := time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local).UnixMilli(), time.Date(2026, 12, 31, 0, 0, 0, 0, time.Local).UnixMilli()
	ok := BillingCheckRequest{Start: start, End: endOfYear, Cps: []BillingCheckCp{{MeteringPoint: "AT003", Direction: consumerDirection}}}
	if err := ok.Validate(); err != nil {
		t.Errorf("one year rejected: %v", err)
	}
	for name, req := range map[string]BillingCheckRequest{
		"start missing":     {End: endOfYear},
		"end before start":  {Start: endOfYear, End: start},
		"longer than year":  {Start: start, End: start + 400*24*3600*1000},
		"huge end":          {Start: start, End: 1 << 62},
		"far future":        {Start: 1 << 62, End: 1 << 62},
		"metering point":    {Start: start, End: start, Cps: []BillingCheckCp{{Direction: consumerDirection}}},
		"invalid direction": {Start: start, End: start, Cps: []BillingCheckCp{{MeteringPoint: "AT003", Direction: "IN"}}},
		"too many":          {Start: start, End: start, Cps: make([]BillingCheckCp, billingCheckMaxCps+1)},
	} {
		if req.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func ptr(t time.Time) *time.Time { return &t }

func sameTime(ms *int64, t *time.Time) bool {
	if ms == nil || t == nil {
		return ms == nil && t == nil
	}
	return *ms == t.UnixMilli()
}

// dayRows returns one consumer row per real quarter hour of the local day (92/96/100), except the
// skipped ones.
func dayRows(mock pgxmock.PgxPoolIface, day time.Time, skip int) *pgxmock.Rows {
	rows := slotRows(mock)
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.Local)
	next := time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, time.Local)
	n := 0
	for ts := start; ts.Before(next); ts = ts.Add(15 * time.Minute) {
		n++
		if n <= skip {
			continue
		}
		rows.AddRow(ts, bcCons, "1-1:1.9.0 G.01", 1.0, int16(1))
	}
	return rows
}

func TestBillingCheck_MissingQuarterHours(t *testing.T) {
	// clock change days in Europe/Vienna also where the machine runs in UTC (CI)
	loc, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = old })
	if quarterHoursOnDay(time.Date(2026, 10, 25, 0, 0, 0, 0, time.Local)) != 100 {
		t.Fatal("25.10.2026 must have 100 quarter hours in Vienna")
	}
	for _, tc := range []struct {
		day     time.Time
		skip    int
		missing int
	}{
		{time.Date(2026, 2, 2, 0, 0, 0, 0, time.Local), 4, 4},
		{time.Date(2026, 3, 29, 0, 0, 0, 0, time.Local), 0, 0},  // 92 quarter hours in Vienna
		{time.Date(2026, 10, 25, 0, 0, 0, 0, time.Local), 0, 0}, // 100 quarter hours in Vienna
	} {
		eng, mock := newMockEngine(t)
		bcMeta(mock)
		end := time.Date(tc.day.Year(), tc.day.Month(), tc.day.Day(), 23, 45, 0, 0, time.Local)
		expectSlots(mock, tc.day, end, dayRows(mock, tc.day, tc.skip))
		res, err := eng.QueryBillingCheck(context.Background(), "vfeeg", "TE100200", &BillingCheckRequest{
			Start: tc.day.UnixMilli(), End: tc.day.UnixMilli(),
			Cps: []BillingCheckCp{bcConsumer(bcCons, 0, 0), bcConsumer("AT0030000000000000000000000000099", 0, 0)},
		})
		if err != nil {
			t.Fatal(err)
		}
		day := tc.day.Format("2006-01-02")
		if m := res.Meters[0]; m.MissingCount != tc.missing || (tc.missing > 0) != (len(m.MissingDays) == 1) {
			t.Errorf("%s: missing %d %v, want %d", day, m.MissingCount, m.MissingDays, tc.missing)
		}
		if m := res.Meters[1]; m.MissingCount != quarterHoursOnDay(tc.day) || len(m.MissingDays) != 0 {
			t.Errorf("%s: unknown metering point missing %d %v", day, m.MissingCount, m.MissingDays)
		}
		mock.Close()
	}
}
