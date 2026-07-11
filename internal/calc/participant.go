package calc

import (
	"sort"
	"time"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/queryengine"
)

// participantConsumer implements queryengine.QueryFunction to drive
// EnergyReportV2: it groups RawSourceLines by calendar day, runs
// AllocDynamicV2 per-day, and appends the resulting allocation +
// share + production values to each ParticipantReport whose meter
// from/until window overlaps the current day.
type participantConsumer struct {
	alloc     AllocationHandlerV2
	report    *ReportResponse
	cpMap     map[string]*counterpoint.CounterPoint
	startDate time.Time
	switchIdx func(currentDate time.Time) int

	info       *queryengine.CounterPointMetaInfo
	daySummary *calcResults
	currentDay time.Time
	dayInit    bool

	// ZVT time-of-use folding (see timewindows.go)
	windows       []timeWindowRange
	dayWindowSums map[string]*calcResults
	bucketSums    map[*MeterReport]map[string]float64
}

func (p *participantConsumer) HandleStart(ctx *queryengine.EngineContext) error {
	p.info = ctx.Info
	p.daySummary = newCalcResult(ctx.Info)
	p.currentDay = p.startDate
	p.windows = distinctWindowRanges(p.report)
	p.dayWindowSums = map[string]*calcResults{}
	p.bucketSums = map[*MeterReport]map[string]float64{}
	return nil
}

func (p *participantConsumer) HandleLine(_ *queryengine.EngineContext, line *queryengine.RawSourceLine) error {
	ts, err := rowIDTime(line.ID)
	if err != nil {
		return nil // skip — mirror v1 silent-skip of bad row IDs
	}

	if !p.dayInit {
		p.currentDay = ts
		p.dayInit = true
	}

	if ts.YearDay() != p.currentDay.YearDay() {
		if err := p.flushDay(p.currentDay); err != nil {
			return err
		}
		p.daySummary = newCalcResult(p.info)
		p.dayWindowSums = map[string]*calcResults{}
		p.currentDay = ts
	}

	// ZVT: fold the quarter-hour into every matching time-of-use window.
	// Membership compares against the wall-clock HH:MM encoded in the row
	// id (local Vienna time, see timewindows.go).
	if len(p.windows) > 0 {
		minuteOfDay := ts.Hour()*60 + ts.Minute()
		for _, w := range p.windows {
			if w.contains(minuteOfDay) {
				ws, ok := p.dayWindowSums[w.rangeKey]
				if !ok {
					ws = newCalcResult(p.info)
					p.dayWindowSums[w.rangeKey] = ws
				}
				if err := appendResults(line, p.alloc, ws); err != nil {
					return err
				}
			}
		}
	}

	return appendResults(line, p.alloc, p.daySummary)
}

func (p *participantConsumer) HandleEnd(_ *queryengine.EngineContext) error {
	if err := p.flushDay(p.currentDay); err != nil {
		return err
	}
	// Final rounding pass (v1 RoundToFixed-loop at end of
	// calcParticipantReport).
	for _, pr := range p.report.ParticipantReports {
		for _, m := range pr.Meters {
			if m.Report != nil {
				m.Report.RoundToFixed(6)
				p.buildBuckets(m)
			}
		}
	}
	return nil
}

// buildBuckets writes the final buckets of one meter: T1/T2 as summed window
// quantities, BASE as the residual against the (rounded) period total - the
// kWh partition is exact by construction (v1 parity).
func (p *participantConsumer) buildBuckets(m *MeterReport) {
	if len(m.TimeWindows) == 0 || m.Report == nil {
		return
	}
	total := m.Report.Summary.Utilization
	if cp, ok := p.cpMap[m.MeterID]; ok && cp.Direction == counterpoint.DirectionProducer {
		total = m.Report.Summary.Production - m.Report.Summary.Allocation
	}
	windows := append([]TimeWindow{}, m.TimeWindows...)
	sort.Slice(windows, func(i, j int) bool { return windows[i].Key < windows[j].Key })

	buckets := make([]Bucket, 0, len(windows)+1)
	windowTotal := float64(0)
	for _, tw := range windows {
		kwh := RoundFixed(p.bucketSums[m][tw.Key], 6)
		windowTotal += kwh
		buckets = append(buckets, Bucket{Key: tw.Key, KWh: kwh})
	}
	base := RoundFixed(total-windowTotal, 6)
	m.Report.Buckets = append([]Bucket{{Key: "BASE", KWh: base}}, buckets...)
}

func (p *participantConsumer) flushDay(day time.Time) error {
	for meterID, cp := range p.cpMap {
		for prIdx := range p.report.ParticipantReports {
			pr := &p.report.ParticipantReports[prIdx]
			for _, m := range pr.Meters {
				if m.MeterID != meterID {
					continue
				}
				from := TruncateToDay(time.UnixMilli(m.From))
				until := TruncateToDay(time.UnixMilli(m.Until))
				if !(from.Unix() <= day.Unix() && day.Unix() <= until.Unix()) {
					continue
				}
				if m.Report == nil {
					// v1-parity: initialize all four intermediate slices as
					// empty arrays (not nil). Go marshals nil slices to JSON
					// `null` which crashes the SPA's `.reduce(...)` call —
					// the frontend expects `[]` for empty time-series.
					m.SetReport(&Report{
						Intermediate: IntermediateRecord{
							Consumption: []float64{},
							Utilization: []float64{},
							Allocation:  []float64{},
							Production:  []float64{},
						},
					})
				}
				switch cp.Direction {
				case counterpoint.DirectionConsumer:
					values := []float64{
						p.daySummary.rCons.RoundToFixed(6).GetElm(cp.SourceIdx, 0),
						p.daySummary.rShar.RoundToFixed(6).GetElm(cp.SourceIdx, 0),
						p.daySummary.rAlloc.RoundToFixed(6).GetElm(cp.SourceIdx, 0),
					}
					m.Report.Summary.Consumption += values[0]
					m.Report.Summary.Allocation += values[1]
					m.Report.Summary.Utilization += values[2]
					p.report.TotalConsumption += values[0]
					p.appendIntermediate(m, values, cp.Direction, day)
				case counterpoint.DirectionProducer:
					values := []float64{
						p.daySummary.rProd.GetElm(cp.SourceIdx, 0),
						p.daySummary.rDist.GetElm(cp.SourceIdx, 0),
					}
					m.Report.Summary.Production += values[0]
					m.Report.Summary.Allocation += values[1]
					p.report.TotalProduction += values[0]
					p.appendIntermediate(m, values, cp.Direction, day)
				}
				p.appendBuckets(m, cp, day)
			}
		}
	}
	return nil
}

// appendBuckets adds the per-window sums of the flushed day to the running
// bucket totals of one meter (v1 appendBucketsToParticipantMeter parity).
// Same day-level from/until gating as the surrounding flushDay loop.
func (p *participantConsumer) appendBuckets(m *MeterReport, cp *counterpoint.CounterPoint, day time.Time) {
	if len(m.TimeWindows) == 0 || len(p.dayWindowSums) == 0 {
		return
	}
	sums, ok := p.bucketSums[m]
	if !ok {
		sums = map[string]float64{}
		p.bucketSums[m] = sums
	}
	for _, tw := range m.TimeWindows {
		ws, ok := p.dayWindowSums[tw.From+"-"+tw.To]
		if !ok {
			continue
		}
		switch cp.Direction {
		case counterpoint.DirectionConsumer:
			// billing quantity of a consumer is the utilization
			sums[tw.Key] += ws.rAlloc.RoundToFixed(6).GetElm(cp.SourceIdx, 0)
		case counterpoint.DirectionProducer:
			// billing quantity of a producer is production - allocation
			sums[tw.Key] += ws.rProd.GetElm(cp.SourceIdx, 0) - ws.rDist.GetElm(cp.SourceIdx, 0)
		}
	}
}

func (p *participantConsumer) appendIntermediate(m *MeterReport, values []float64,
	dir counterpoint.Direction, day time.Time) {
	idx := p.switchIdx(day)
	m.Report.Intermediate.ID = "IRP/2023/01"

	switch dir {
	case counterpoint.DirectionConsumer:
		m.Report.Intermediate.Consumption = ensureFloatSlice(m.Report.Intermediate.Consumption, idx)
		m.Report.Intermediate.Allocation = ensureFloatSlice(m.Report.Intermediate.Allocation, idx)
		m.Report.Intermediate.Utilization = ensureFloatSlice(m.Report.Intermediate.Utilization, idx)
		m.Report.Intermediate.Consumption[idx-1] = RoundFixed(m.Report.Intermediate.Consumption[idx-1]+values[0], 6)
		m.Report.Intermediate.Allocation[idx-1] = RoundFixed(m.Report.Intermediate.Allocation[idx-1]+values[1], 6)
		m.Report.Intermediate.Utilization[idx-1] = RoundFixed(m.Report.Intermediate.Utilization[idx-1]+values[2], 6)
	case counterpoint.DirectionProducer:
		m.Report.Intermediate.Production = ensureFloatSlice(m.Report.Intermediate.Production, idx)
		m.Report.Intermediate.Allocation = ensureFloatSlice(m.Report.Intermediate.Allocation, idx)
		m.Report.Intermediate.Production[idx-1] += values[0]
		m.Report.Intermediate.Allocation[idx-1] += values[1]
	}
}
