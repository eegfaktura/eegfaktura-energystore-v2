package api

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/calc"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/queryengine"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/store"
)

func newFullServer(t *testing.T) (*Server, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	st := store.FromPool(mock)
	repo := counterpoint.NewRepository(mock)
	srv := NewWithOptions(st, Options{
		QueryEngine: queryengine.New(mock, repo),
		Calc:        calc.New(mock, repo),
	})
	return srv, mock
}

func TestEEG_RawV2_NoCpReturnsEmpty(t *testing.T) {
	srv, _ := newFullServer(t)
	body := strings.NewReader(`{"start":1,"end":2}`)
	req := httptest.NewRequest(http.MethodPost, "/eeg/v2/TE100200/raw", body)
	req.Header.Set("X-Tenant", "vfeeg")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "{}\n" {
		t.Fatalf("expected {}, got %q", rec.Body.String())
	}
}

func TestEEG_CombinedReport_EmptyReports(t *testing.T) {
	srv, _ := newFullServer(t)
	body := strings.NewReader(`{"start":1,"end":2,"reports":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/eeg/v2/TE100200/combined-report", body)
	req.Header.Set("X-Tenant", "vfeeg")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "null") {
		t.Fatalf("expected null, got %q", rec.Body.String())
	}
}

func TestEEG_LoadCurve_EmptyRange(t *testing.T) {
	srv, _ := newFullServer(t)
	body := strings.NewReader(`{"start":0,"end":0}`)
	req := httptest.NewRequest(http.MethodPost, "/eeg/v2/TE100200/load-curve-report", body)
	req.Header.Set("X-Tenant", "vfeeg")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "[]") {
		t.Fatalf("expected [], got %q", rec.Body.String())
	}
}

func TestEEG_ExcelExport_NoExcelEngine(t *testing.T) {
	srv, _ := newFullServer(t) // no Excel engine injected
	req := httptest.NewRequest(http.MethodPost, "/eeg/TE100200/excel/export/2026/06", nil)
	req.Header.Set("X-Tenant", "vfeeg")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestEEG_Summary_RoundTrip(t *testing.T) {
	srv, mock := newFullServer(t)
	defer mock.Close()

	// EnergySummary → counterpoint.ListByEC + slots query.
	now := time.Now()
	mock.ExpectQuery(`FROM counterpoint_meta`).
		WithArgs("vfeeg", "TE100200").
		WillReturnRows(mock.NewRows([]string{
			"tenant_id", "ec_id", "metering_point", "direction", "source_idx",
			"period_start", "period_end", "payload", "updated_at",
		}).AddRow("vfeeg", "TE100200", "AT_CON",
			int16(counterpoint.DirectionConsumer), 0,
			(*time.Time)(nil), (*time.Time)(nil), []byte(`{}`), now))
	start, end, _ := calc.PeriodToStartEndTime(2026, 6, "YM")
	mock.ExpectQuery(`FROM energy_data`).
		WithArgs("vfeeg", "TE100200", start, end).
		WillReturnRows(mock.NewRows([]string{"ts", "metering_point", "meter_code", "value", "qov"}))

	body := bytes.NewBufferString(`{"year":2026,"segment":6,"type":"YM"}`)
	req := httptest.NewRequest(http.MethodPost, "/eeg/v2/TE100200/summary", body)
	req.Header.Set("X-Tenant", "vfeeg")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Body.String(), "[") {
		t.Fatalf("expected array wrap, got %q", rec.Body.String())
	}
}

func TestEEG_LastRecordDate_NoMeta(t *testing.T) {
	srv, mock := newFullServer(t)
	defer mock.Close()
	mock.ExpectQuery(`FROM counterpoint_meta`).
		WithArgs("vfeeg", "TE100200").
		WillReturnRows(mock.NewRows([]string{
			"tenant_id", "ec_id", "metering_point", "direction", "source_idx",
			"period_start", "period_end", "payload", "updated_at",
		}))
	req := httptest.NewRequest(http.MethodGet, "/eeg/TE100200/lastRecordDate", nil)
	req.Header.Set("X-Tenant", "vfeeg")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestEEG_BillingCheck_InvalidRequest(t *testing.T) {
	srv, _ := newFullServer(t)
	for _, body := range []string{`{`, `{"start":0,"end":1}`, `{"start":1,"end":2,"cps":[{"meteringPoint":"AT1","direction":"IN"}]}`} {
		req := httptest.NewRequest(http.MethodPost, "/eeg/v2/TE100200/billing-check", strings.NewReader(body))
		req.Header.Set("X-Tenant", "vfeeg")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d", body, rec.Code)
		}
	}
}

func TestEEG_BillingCheck_WithData(t *testing.T) {
	srv, mock := newFullServer(t)
	now := time.Now()
	mock.ExpectQuery(`FROM counterpoint_meta`).WithArgs("vfeeg", "TE100200").
		WillReturnRows(mock.NewRows([]string{"tenant_id", "ec_id", "metering_point", "direction", "source_idx",
			"period_start", "period_end", "payload", "updated_at"}).
			AddRow("vfeeg", "TE100200", "AT1", int16(counterpoint.DirectionConsumer), 0,
				(*time.Time)(nil), (*time.Time)(nil), []byte(`{}`), now))
	start := time.Date(2026, 2, 1, 0, 0, 0, 0, time.Local)
	end := time.Date(2026, 2, 28, 0, 0, 0, 0, time.Local)
	ts := time.Date(2026, 2, 10, 8, 0, 0, 0, time.Local)
	mock.ExpectQuery(`FROM energy_data`).
		WithArgs("vfeeg", "TE100200", start, time.Date(2026, 2, 28, 23, 45, 0, 0, time.Local)).
		WillReturnRows(mock.NewRows([]string{"ts", "metering_point", "meter_code", "value", "qov"}).
			AddRow(ts, "AT1", "1-1:1.9.0 G.01", 1.5, int16(3)))
	body := fmt.Sprintf(`{"start":%d,"end":%d,"cps":[{"meteringPoint":"AT1","direction":"CONSUMPTION","activeSince":0,"inactiveSince":0},{"meteringPoint":"AT9","direction":"CONSUMPTION","activeSince":0,"inactiveSince":0}]}`,
		start.UnixMilli(), end.UnixMilli())
	req := httptest.NewRequest(http.MethodPost, "/eeg/v2/TE100200/billing-check", strings.NewReader(body))
	req.Header.Set("X-Tenant", "vfeeg")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	out := rec.Body.String()
	for _, want := range []string{`"l3Count":1`, `"l3Days":["2026-02-10"]`, `"l3Days":[]`, `"firstData":null`, `"consumed":1.5`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %d %s", want, rec.Code, out)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestEEG_BillingCheck_EmptyCps(t *testing.T) {
	srv, mock := newFullServer(t)
	mock.ExpectQuery(`FROM counterpoint_meta`).WithArgs("vfeeg", "TE100200").
		WillReturnRows(mock.NewRows([]string{"tenant_id", "ec_id", "metering_point", "direction", "source_idx",
			"period_start", "period_end", "payload", "updated_at"}))
	req := httptest.NewRequest(http.MethodPost, "/eeg/v2/TE100200/billing-check",
		strings.NewReader(`{"start":1767222000000,"end":1769814000000,"cps":[]}`))
	req.Header.Set("X-Tenant", "vfeeg")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"meters":[]`) {
		t.Fatalf("expected 200 with no meters, got %d %s", rec.Code, rec.Body.String())
	}
}
