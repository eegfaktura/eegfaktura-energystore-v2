package calc

import (
	"math"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
)

// Recort (sic) is v1's name for a per-period numeric summary attached to
// every meter / participant. Field order + tags are wire-bearing.
type Recort struct {
	Consumption float64 `json:"consumption"`
	Utilization float64 `json:"utilization"`
	Allocation  float64 `json:"allocation"`
	Production  float64 `json:"production"`
}

// RoundToFixed quantises every field to `precision` decimals.
func (r *Recort) RoundToFixed(precision uint) {
	ratio := math.Pow(10, float64(precision))
	r.Utilization = math.Round(r.Utilization*ratio) / ratio
	r.Consumption = math.Round(r.Consumption*ratio) / ratio
	r.Allocation = math.Round(r.Allocation*ratio) / ratio
	r.Production = math.Round(r.Production*ratio) / ratio
}

// IntermediateRecord is the per-bucket time-series of values inside a
// participant report. v1 grows the slices on demand (EnsureIntermediate*).
type IntermediateRecord struct {
	ID          string    `json:"id"`
	Consumption []float64 `json:"consumption"`
	Utilization []float64 `json:"utilization"`
	Allocation  []float64 `json:"allocation"`
	Production  []float64 `json:"production"`
}

// TimeWindow is a generic time-of-use window (wall-clock, 15-min raster).
// From is inclusive, To exclusive; From > To means the window crosses
// midnight. energystore has no tariff/price knowledge - windows are plain
// daytime ranges. Wire shape shared with v1.
type TimeWindow struct {
	Key  string `json:"key"`  // "T1" | "T2"
	From string `json:"from"` // "HH:MM"
	To   string `json:"to"`   // "HH:MM"
}

// Bucket is the per-window energy sum of the billing quantity of one meter.
// BASE is the residual (period total minus all window sums), so the kWh
// partition is exact by construction. Wire shape shared with v1.
type Bucket struct {
	Key string  `json:"key"` // "BASE" | "T1" | "T2"
	KWh float64 `json:"kWh"`
}

// Report bundles summary + intermediate.
type Report struct {
	ID           string             `json:"id"`
	Summary      Recort             `json:"summary"`
	Intermediate IntermediateRecord `json:"intermediate"`
	Buckets      []Bucket           `json:"buckets,omitempty"`
}

// RoundToFixed quantises Summary; Intermediate slices are quantised
// element-wise where v1 already calls RoundToFixed in-place.
func (rp *Report) RoundToFixed(precision uint) {
	rp.Summary.RoundToFixed(precision)
}

// MeterReport mirrors v1 wire shape exactly.
type MeterReport struct {
	MeterID     string       `json:"meterId"`
	MeterDir    string       `json:"meterDir"`
	From        int64        `json:"from"`
	Until       int64        `json:"until"`
	TimeWindows []TimeWindow `json:"timeWindows,omitempty"`
	Report      *Report      `json:"report"`
}

// SetReport mirrors v1.
func (m *MeterReport) SetReport(r *Report) { m.Report = r }

// ParticipantReport is the meter-grouping unit handed in by the caller.
type ParticipantReport struct {
	ParticipantID string         `json:"participantId"`
	Meters        []*MeterReport `json:"meters"`
}

// ReportResponse is the /eeg/v2/{ec}/report payload. Field order + tags
// are wire-bearing.
type ReportResponse struct {
	ID                 string                       `json:"id"`
	ParticipantReports []ParticipantReport          `json:"participantReports"`
	Meta               []*counterpoint.CounterPoint `json:"meta"`
	TotalProduction    float64                      `json:"totalProduction"`
	TotalConsumption   float64                      `json:"totalConsumption"`
}

// EnergyReport is the v1-legacy /eeg/report shape.
type EnergyReport struct {
	ID            string    `json:"id"`
	Allocated     []float64 `json:"allocated"`
	Consumed      []float64 `json:"consumed"`
	Produced      []float64 `json:"produced"`
	Distributed   []float64 `json:"distributed"`
	Shared        []float64 `json:"shared"`
	TotalProduced float64   `json:"total_produced"`
}

// EegEnergy is the v1-legacy /eeg/report wrapper.
type EegEnergy struct {
	Report  *EnergyReport                `json:"report"`
	Results []*EnergyReport              `json:"intermediateReportResults"`
	Meta    []*counterpoint.CounterPoint `json:"meta"`
}

// EnergyReportRequest mirrors the v1 request body.
type EnergyReportRequest struct {
	Year    int    `json:"year"`
	Period  string `json:"type"`
	Segment int    `json:"segment"`
}

// ReportRequest is the v1 /eeg/v2/{ec}/report POST body shape.
type ReportRequest struct {
	ReportInterval EnergyReportRequest `json:"reportInterval"`
	Participants   []ParticipantReport `json:"participants"`
}
