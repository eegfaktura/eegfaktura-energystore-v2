package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/auth"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/calc"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/excelexport"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/queryengine"
)

// eegRoutes registers the v1-parity /eeg/* + /query/* endpoints.
func (s *Server) eegRoutes() {
	s.handle("POST /eeg/report", s.protect(s.handleEnergyReport))
	s.handle("POST /eeg/v2/{ecid}/report", s.protect(s.handleEnergyReportV2))
	s.handle("POST /eeg/v2/{ecid}/intradayreport", s.protect(s.handleIntraDayReport))
	s.handle("POST /eeg/v2/{ecid}/intra-day-report", s.protect(s.handleIntraDayReport))
	s.handle("POST /eeg/v2/{ecid}/summary", s.protect(s.handleSummary))
	s.handle("POST /eeg/v2/{ecid}/load-curve-report", s.protect(s.handleLoadCurveReport))
	s.handle("POST /eeg/v2/{ecid}/combined-report", s.protect(s.handleCombinedReport))
	s.handle("POST /eeg/v2/{ecid}/raw", s.protect(s.handleRawV2))
	s.handle("GET /eeg/v2/{ecid}/meta", s.protect(s.handleMetaV2))
	s.handle("GET /eeg/{ecid}/lastRecordDate", s.protect(s.handleEEGLastRecordDate))
	s.handle("POST /eeg/{ecid}/excel/export/{year}/{month}", s.protect(s.handleExcelExport))
	s.handle("POST /eeg/{ecid}/excel/report/download", s.protect(s.handleExcelDownload))

	// /query/* — Basic-Auth-protected. Same wire shape as v1.
	s.handle("POST /query/rawdata", s.protectAPI(s.handleQueryRawData))
	s.handle("POST /query/{ecid}/metadata", s.protectAPI(s.handleQueryMetadata))
}

// handleEnergyReport is the v1 /eeg/report path (Y/YM/YQ/YH).
func (s *Server) handleEnergyReport(w http.ResponseWriter, r *http.Request, _ *auth.PlatformClaims, tenant string) {
	if s.calc == nil {
		writeError(w, http.StatusServiceUnavailable, "calc engine not configured")
		return
	}
	var req calc.EnergyReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := s.calc.EnergyReport(r.Context(), tenant, req.Year, req.Segment, req.Period)
	if err != nil {
		s.logger.Error("EnergyReport", "err", err, "tenant", tenant)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Eeg *calc.EegEnergy `json:"eeg"`
	}{Eeg: out})
}

func (s *Server) handleEnergyReportV2(w http.ResponseWriter, r *http.Request, _ *auth.PlatformClaims, tenant string) {
	if s.calc == nil {
		writeError(w, http.StatusServiceUnavailable, "calc engine not configured")
		return
	}
	ecid := r.PathValue("ecid")
	var req calc.ReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := calc.ValidateTimeWindows(req.Participants); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp := &calc.ReportResponse{ParticipantReports: req.Participants}
	if err := s.calc.EnergyReportV2(r.Context(), tenant, ecid,
		req.ReportInterval.Year, req.ReportInterval.Segment, req.ReportInterval.Period, resp); err != nil {
		s.logger.Error("EnergyReportV2", "err", err, "tenant", tenant)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// rangeBody is the common {start, end} epoch-ms shape used by the chart
// endpoints.
type rangeBody struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

func (s *Server) handleIntraDayReport(w http.ResponseWriter, r *http.Request, _ *auth.PlatformClaims, tenant string) {
	if s.qe == nil {
		writeError(w, http.StatusServiceUnavailable, "query engine not configured")
		return
	}
	ecid := r.PathValue("ecid")
	var body rangeBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, err := s.qe.QueryIntraDayReport(r.Context(), tenant, ecid,
		time.UnixMilli(body.Start), time.UnixMilli(body.End))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request, _ *auth.PlatformClaims, tenant string) {
	if s.calc == nil {
		writeError(w, http.StatusServiceUnavailable, "calc engine not configured")
		return
	}
	ecid := r.PathValue("ecid")
	var req calc.EnergyReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, err := s.calc.EnergySummary(r.Context(), tenant, ecid, req.Year, req.Segment, req.Period)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// v1 wraps in a 1-element array to match prod-image shape (the
	// customer-web SPA at energy.service.ts:146 does res[0]).
	writeJSON(w, http.StatusOK, []*queryengine.ReportData{resp})
}

func (s *Server) handleLoadCurveReport(w http.ResponseWriter, r *http.Request, _ *auth.PlatformClaims, tenant string) {
	if s.qe == nil {
		writeError(w, http.StatusServiceUnavailable, "query engine not configured")
		return
	}
	ecid := r.PathValue("ecid")
	var body rangeBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Start == 0 || body.End == 0 {
		writeJSON(w, http.StatusOK, []*queryengine.ReportData{})
		return
	}
	start := time.UnixMilli(body.Start)
	end := time.UnixMilli(body.End)
	resp, err := pickLoadCurve(r.Context(), s.qe, tenant, ecid, start, end)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// pickLoadCurve dispatches between Daily, Weekly and Monthly LoadCurve
// based on the requested range — matches the period-based aggregation
// the customer-web property popover triggers via /load-curve-report and
// the dashboard initialiser via /combined-report. Prod sample headers:
//
//	Period   Range    prod-Name shape
//	-------  ------   ----------------
//	Monat    ~30 d    D:MM:DD:DOW (daily)
//	Quartal  ~91 d    W:YYYY:WW:WW (ISO-week)
//	Halbjahr ~181 d   M:YYYY:MM:MM (calendar month)
//	Jahr     ~365 d   M:YYYY:MM:MM (calendar month)
//
// Threshold picks: > 120 d → monthly (covers Halbjahr 181d + Year 365d),
// 50 d < range ≤ 120 d → weekly (Quartal 91d), ≤ 50 d → daily (Monat).
func pickLoadCurve(ctx context.Context, qe *queryengine.Engine,
	tenant, ecid string, start, end time.Time) ([]*queryengine.ReportData, error) {
	const (
		monthlySwitchDays = 120
		weeklySwitchDays  = 50
	)
	d := end.Sub(start)
	switch {
	case d > time.Duration(monthlySwitchDays)*24*time.Hour:
		return qe.QueryMonthlyCurveReport(ctx, tenant, ecid, start, end)
	case d > time.Duration(weeklySwitchDays)*24*time.Hour:
		return qe.QueryWeeklyCurveReport(ctx, tenant, ecid, start, end)
	default:
		return qe.QueryLoadCurveReport(ctx, tenant, ecid, start, end)
	}
}

// handleCombinedReport implements the dashboard's combined fetch:
// reports: ["intraday","loadcurve"] returns an array of
// {reportName, reportData} entries.
func (s *Server) handleCombinedReport(w http.ResponseWriter, r *http.Request, _ *auth.PlatformClaims, tenant string) {
	if s.qe == nil {
		writeError(w, http.StatusServiceUnavailable, "query engine not configured")
		return
	}
	ecid := r.PathValue("ecid")
	var body struct {
		Start   int64    `json:"start"`
		End     int64    `json:"end"`
		Reports []string `json:"reports"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body.Reports) == 0 || body.Start == 0 || body.End == 0 {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	start := time.UnixMilli(body.Start)
	end := time.UnixMilli(body.End)

	type entry struct {
		ReportName string                    `json:"reportName"`
		ReportData []*queryengine.ReportData `json:"reportData"`
	}
	result := make([]entry, 0, len(body.Reports))
	for _, name := range body.Reports {
		var data []*queryengine.ReportData
		var err error
		switch name {
		case "intraday":
			data, err = s.qe.QueryIntraDayReport(r.Context(), tenant, ecid, start, end)
		case "loadcurve":
			data, err = pickLoadCurve(r.Context(), s.qe, tenant, ecid, start, end)
		default:
			continue
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, name+": "+err.Error())
			return
		}
		result = append(result, entry{ReportName: name, ReportData: data})
	}
	writeJSON(w, http.StatusOK, result)
}

// handleRawV2 implements the v1 prod-image quirk of returning `{}` when
// no `cp` query parameter is supplied. Otherwise delegates to
// QueryRawData.
func (s *Server) handleRawV2(w http.ResponseWriter, r *http.Request, _ *auth.PlatformClaims, tenant string) {
	if s.qe == nil {
		writeError(w, http.StatusServiceUnavailable, "query engine not configured")
		return
	}
	ecid := r.PathValue("ecid")
	var body rangeBody
	_ = json.NewDecoder(r.Body).Decode(&body) // body parse failures map to empty body

	params := r.URL.Query()
	cps := []queryengine.TargetMP{}
	if rawCps, ok := params["cp"]; ok {
		for _, c := range rawCps {
			cps = append(cps, queryengine.TargetMP{MeteringPoint: c})
		}
	}
	if len(cps) == 0 || body.Start == 0 || body.End == 0 {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	res, err := s.qe.QueryRawData(r.Context(), tenant, ecid,
		time.UnixMilli(body.Start), time.UnixMilli(body.End), cps, params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleMetaV2(w http.ResponseWriter, r *http.Request, _ *auth.PlatformClaims, tenant string) {
	if s.qe == nil {
		writeError(w, http.StatusServiceUnavailable, "query engine not configured")
		return
	}
	ecid := r.PathValue("ecid")
	meta, err := s.qe.QueryMetaData(r.Context(), tenant, ecid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

// handleEEGLastRecordDate is the v1 path `/eeg/{ecid}/lastRecordDate`
// returning `{"periodEnd": ts}`. The mp+code combination v1 effectively
// queries is the consumer "1-1:1.9.0 G.01" — the per-EC global earliest
// receive time. v2 keeps the same semantics by picking the most recent
// G.01 entry across all consumer metering points in the EC.
func (s *Server) handleEEGLastRecordDate(w http.ResponseWriter, r *http.Request, _ *auth.PlatformClaims, tenant string) {
	if s.qe == nil {
		writeError(w, http.StatusServiceUnavailable, "query engine not configured")
		return
	}
	ecid := r.PathValue("ecid")
	// MetaData carries per-MP period bounds; the latest PeriodEnd across
	// consumers is the v1-equivalent "lastRecordDate" — it's the
	// netzbetreiber-reported window upper bound.
	meta, err := s.qe.QueryMetaData(r.Context(), tenant, ecid)
	if err != nil {
		writeError(w, http.StatusNotFound, "No entry found")
		return
	}
	var latest int64
	for _, m := range meta {
		if m.PeriodEnd > latest {
			latest = m.PeriodEnd
		}
	}
	if latest == 0 {
		writeError(w, http.StatusNotFound, "No entry found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"periodEnd": time.UnixMilli(latest).UTC().Format(time.RFC3339)})
}

// handleExcelExport is the v1 monthly path (/eeg/{ecid}/excel/export/{year}/{month}).
// v1 emails the file; v2 returns the file as the response body. The
// request body is the ExportCPs JSON.
func (s *Server) handleExcelExport(w http.ResponseWriter, r *http.Request, _ *auth.PlatformClaims, tenant string) {
	if s.excel == nil {
		writeError(w, http.StatusServiceUnavailable, "excel engine not configured")
		return
	}
	year, err := strconv.Atoi(r.PathValue("year"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid year")
		return
	}
	month, err := strconv.Atoi(r.PathValue("month"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid month")
		return
	}
	ecid := r.PathValue("ecid")
	var cps excelexport.ExportCPs
	if err := json.NewDecoder(r.Body).Decode(&cps); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	buf, err := s.excel.ExportEnergyForMonth(r.Context(), tenant, ecid, year, month, &cps)
	if err != nil {
		s.logger.Error("excel export", "err", err, "tenant", tenant)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+tenant+`-Energie-Report-`+
			r.PathValue("year")+r.PathValue("month")+`.xlsx"`)
	_, _ = buf.WriteTo(w)
}

// handleExcelDownload is the arbitrary-range path
// (/eeg/{ecid}/excel/report/download). Body carries start/end (epoch
// ms) + cps + communityId.
func (s *Server) handleExcelDownload(w http.ResponseWriter, r *http.Request, _ *auth.PlatformClaims, tenant string) {
	if s.excel == nil {
		writeError(w, http.StatusServiceUnavailable, "excel engine not configured")
		return
	}
	ecid := r.PathValue("ecid")
	var cps excelexport.ExportCPs
	if err := json.NewDecoder(r.Body).Decode(&cps); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	start := time.UnixMilli(cps.Start)
	end := time.UnixMilli(cps.End)
	buf, err := s.excel.ExportEnergyToExcel(r.Context(), tenant, ecid, start, end, &cps)
	if err != nil {
		s.logger.Error("excel download", "err", err, "tenant", tenant)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+tenant+`-Energy-Report-`+
			start.Format("20060102")+`_`+end.Format("20060102")+`.xlsx"`)
	_, _ = buf.WriteTo(w)
}

// handleQueryRawData is the Basic-Auth /query/rawdata path. Wire-shape
// identical to v1.
func (s *Server) handleQueryRawData(w http.ResponseWriter, r *http.Request, _ *auth.PlatformClaims, tenant string) {
	if s.qe == nil {
		writeError(w, http.StatusServiceUnavailable, "query engine not configured")
		return
	}
	var req struct {
		Cps   []queryengine.TargetMP `json:"cps"`
		EcID  string                 `json:"ecId"`
		Start int64                  `json:"start"`
		End   int64                  `json:"end"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.qe.QueryRawData(r.Context(), tenant, req.EcID,
		time.UnixMilli(req.Start), time.UnixMilli(req.End), req.Cps, r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleQueryMetadata(w http.ResponseWriter, r *http.Request, _ *auth.PlatformClaims, tenant string) {
	if s.qe == nil {
		writeError(w, http.StatusServiceUnavailable, "query engine not configured")
		return
	}
	ecid := r.PathValue("ecid")
	meta, err := s.qe.QueryMetaData(r.Context(), tenant, ecid)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, meta)
}
