package queryengine

import (
	"context"
	"errors"
	"time"
)

// QueryIntraDayReport: mirror v1 store.QueryIntraDayReport.
func (e *Engine) QueryIntraDayReport(ctx context.Context, tenant, ec string,
	start, end time.Time) ([]*ReportData, error) {
	id := NewIntraDayFunction()
	if err := e.Query(ctx, tenant, ec, start, end, id); err != nil {
		if errors.Is(err, ErrNoRows) {
			return id.GetResult(), nil
		}
		return nil, err
	}
	return id.GetResult(), nil
}

// QueryLoadCurveReport: mirror v1 store.QueryLoadCurveReport. v1 returns
// an empty slice on "no Rows found"; we do the same.
func (e *Engine) QueryLoadCurveReport(ctx context.Context, tenant, ec string,
	start, end time.Time) ([]*ReportData, error) {
	lc := NewLoadCurveFunction()
	if err := e.Query(ctx, tenant, ec, start, end, lc); err != nil {
		if errors.Is(err, ErrNoRows) {
			return []*ReportData{}, nil
		}
		return nil, err
	}
	return lc.GetResult(), nil
}

// QueryWeeklyCurveReport: aggregate per ISO calendar week, emitting
// `W:YYYY:WW:WW`-shaped names. Used by the combined/load-curve dispatch
// for medium-range queries (Quartal / Halbjahr).
func (e *Engine) QueryWeeklyCurveReport(ctx context.Context, tenant, ec string,
	start, end time.Time) ([]*ReportData, error) {
	wc := NewWeeklyCurveFunction()
	if err := e.Query(ctx, tenant, ec, start, end, wc); err != nil {
		if errors.Is(err, ErrNoRows) {
			return []*ReportData{}, nil
		}
		return nil, err
	}
	return wc.GetResult(), nil
}

// QueryMonthlyCurveReport: aggregate per calendar month, emitting
// `M:YYYY:MM:00` shaped names. Used by the combined-report dispatcher
// when the requested range is too long for daily granularity to be
// meaningful in a single chart (e.g. Year view → 12 month bars).
func (e *Engine) QueryMonthlyCurveReport(ctx context.Context, tenant, ec string,
	start, end time.Time) ([]*ReportData, error) {
	mc := NewMonthlyCurveFunction()
	if err := e.Query(ctx, tenant, ec, start, end, mc); err != nil {
		if errors.Is(err, ErrNoRows) {
			return []*ReportData{}, nil
		}
		return nil, err
	}
	return mc.GetResult(), nil
}

// QuerySummaryReport: mirror v1 store.NewEnergySummary path.
func (e *Engine) QuerySummaryReport(ctx context.Context, tenant, ec string,
	start, end time.Time) (*ReportData, error) {
	s := NewSummary()
	if err := e.Query(ctx, tenant, ec, start, end, s); err != nil {
		if errors.Is(err, ErrNoRows) {
			return s.GetResult(), nil
		}
		return nil, err
	}
	return s.GetResult(), nil
}

// QueryRawData: mirror v1 store.QueryRawData. The `params` map carries
// the v1 `f=...` query string; when absent, DefaultFunction is used.
func (e *Engine) QueryRawData(ctx context.Context, tenant, ec string,
	start, end time.Time, cps []TargetMP,
	params map[string][]string) (map[string]*RawDataResult, error) {
	cps = dedupTargets(cps)
	var fn QueryFunction
	if vs, ok := params["f"]; ok && len(vs) > 0 {
		name, args, err := ParseRawFunction(vs[0])
		if err != nil {
			return nil, err
		}
		factory, ok := RawFunctions[name]
		if !ok {
			return nil, errors.New("queryengine: unknown function " + name)
		}
		f, ferr := factory(args, cps)
		if ferr != nil {
			return nil, ferr
		}
		fn = f
	} else {
		fn = NewDefaultFunction(cps)
	}

	if err := e.Query(ctx, tenant, ec, start, end, fn); err != nil {
		return nil, err
	}
	// All raw-result-producing functions implement getResult via
	// ParentFunction. Type-assert; if the function doesn't carry one
	// (Summary, IntraDay, LoadCurve) it's a programmer error in the
	// callers — fall back to nil.
	type withMapResult interface {
		GetResult() map[string]*RawDataResult
	}
	if r, ok := fn.(withMapResult); ok {
		return r.GetResult(), nil
	}
	return nil, errors.New("queryengine: function does not produce per-MP results")
}

// dedupTargets removes duplicate metering points from the target list,
// keeping the first occurrence and preserving order. The store holds exactly
// one series per metering-point name, so a caller that lists the same ZP more
// than once — e.g. a metering point re-registered under a new member while the
// old, overlapping participant row is still listed — would otherwise get that
// ZP's identical series appended once per duplicate entry (ParentFunction) or
// double-counted (Aggregate). Mirrors the v1 store.dedupTargets fix.
func dedupTargets(cps []TargetMP) []TargetMP {
	seen := make(map[string]struct{}, len(cps))
	out := make([]TargetMP, 0, len(cps))
	for _, cp := range cps {
		if _, ok := seen[cp.MeteringPoint]; ok {
			continue
		}
		seen[cp.MeteringPoint] = struct{}{}
		out = append(out, cp)
	}
	return out
}

// QueryMetaData mirrors v1 store.QueryMetaData. PeriodBegin/End come from
// counterpoint_meta.period_{start,end}; metering points without periods
// are excluded (matches v1 NullPeriod-skip path).
func (e *Engine) QueryMetaData(ctx context.Context, tenant, ec string) (map[string]*MetaData, error) {
	cps, err := e.repo.ListByEC(ctx, tenant, ec)
	if err != nil {
		return nil, err
	}
	out := map[string]*MetaData{}
	for _, cp := range cps {
		if cp.PeriodStart == nil || cp.PeriodEnd == nil {
			continue
		}
		out[cp.MeteringPoint] = &MetaData{
			PeriodBegin: cp.PeriodStart.UnixMilli(),
			PeriodEnd:   cp.PeriodEnd.UnixMilli(),
		}
	}
	return out, nil
}
