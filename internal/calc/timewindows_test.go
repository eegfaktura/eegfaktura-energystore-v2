package calc

import (
	"testing"
	"time"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/queryengine"
)

func TestParseWindowTime(t *testing.T) {
	m, err := parseWindowTime("06:15")
	if err != nil {
		t.Fatalf("06:15: unexpected error %v", err)
	}
	if m != 6*60+15 {
		t.Fatalf("06:15: got %d", m)
	}
	for _, bad := range []string{"06:07", "24:00", "nonsense"} {
		if _, err := parseWindowTime(bad); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
}

func TestTimeWindowContains(t *testing.T) {
	day, err := parseTimeWindow(TimeWindow{Key: "T1", From: "06:00", To: "08:00"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		min  int
		want bool
	}{
		{6 * 60, true}, {7*60 + 45, true}, {8 * 60, false}, {5*60 + 45, false},
	}
	for _, c := range cases {
		if got := day.contains(c.min); got != c.want {
			t.Errorf("day window contains(%d) = %v, want %v", c.min, got, c.want)
		}
	}

	night, err := parseTimeWindow(TimeWindow{Key: "T2", From: "20:00", To: "06:00"})
	if err != nil {
		t.Fatal(err)
	}
	nightCases := []struct {
		min  int
		want bool
	}{
		{20 * 60, true}, {23*60 + 45, true}, {0, true}, {5*60 + 45, true},
		{6 * 60, false}, {12 * 60, false},
	}
	for _, c := range nightCases {
		if got := night.contains(c.min); got != c.want {
			t.Errorf("night window contains(%d) = %v, want %v", c.min, got, c.want)
		}
	}
}

func TestValidateTimeWindows(t *testing.T) {
	mk := func(windows []TimeWindow) []ParticipantReport {
		return []ParticipantReport{{
			ParticipantID: "P1",
			Meters:        []*MeterReport{{MeterID: "M1", TimeWindows: windows}},
		}}
	}

	if err := ValidateTimeWindows(mk(nil)); err != nil {
		t.Errorf("nil windows: unexpected error %v", err)
	}
	if err := ValidateTimeWindows(mk([]TimeWindow{
		{Key: "T1", From: "06:00", To: "08:00"},
		{Key: "T2", From: "20:00", To: "06:00"},
	})); err != nil {
		t.Errorf("valid windows: unexpected error %v", err)
	}

	invalid := map[string][]TimeWindow{
		"duplicate key": {
			{Key: "T1", From: "06:00", To: "08:00"},
			{Key: "T1", From: "10:00", To: "11:00"},
		},
		"invalid key":  {{Key: "BASE", From: "06:00", To: "08:00"}},
		"from == to":   {{Key: "T1", From: "06:00", To: "06:00"}},
		"raster":       {{Key: "T1", From: "06:05", To: "08:00"}},
		"more than 2": {
			{Key: "T1", From: "06:00", To: "07:00"},
			{Key: "T2", From: "08:00", To: "09:00"},
			{Key: "T1", From: "10:00", To: "11:00"},
		},
	}
	for name, windows := range invalid {
		if err := ValidateTimeWindows(mk(windows)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// TestParticipantConsumerBuckets folds a hand-computable quarter-hour series
// into time-of-use buckets: consumer with T1 (06:00-08:00) + midnight-crossing
// T2 (20:00-06:00), producer with T1 only. BASE must be the exact residual.
// Mirrors the v1 test (calculation/timeWindows_test.go).
func TestParticipantConsumerBuckets(t *testing.T) {
	// line layout: Consumers = [consumption, allocation, utilization] per
	// consumer; Producers = [production, allocation] per producer.
	mkLine := func(id string, util, prod, dist float64) *queryengine.RawSourceLine {
		return &queryengine.RawSourceLine{
			ID:        id,
			Consumers: []float64{util, 0, util},
			Producers: []float64{prod, dist},
		}
	}
	lines := []*queryengine.RawSourceLine{
		mkLine("CP/2024/01/01/05/45", 7, 0, 0),  // T2 (morning side of midnight window)
		mkLine("CP/2024/01/01/06/00", 2, 10, 4), // T1 (from inclusive)
		mkLine("CP/2024/01/01/07/45", 3, 0, 0),  // T1
		mkLine("CP/2024/01/01/08/00", 4, 0, 0),  // BASE (to exclusive)
		mkLine("CP/2024/01/01/12/00", 1, 20, 5), // BASE
		mkLine("CP/2024/01/01/20/00", 5, 0, 0),  // T2 (from inclusive)
		mkLine("CP/2024/01/01/23/45", 6, 0, 0),  // T2
		mkLine("CP/2024/01/02/00/00", 8, 0, 0),  // T2, next day
	}

	from := time.Date(2024, 1, 1, 0, 0, 0, 0, time.Local).UnixMilli()
	until := time.Date(2024, 12, 31, 0, 0, 0, 0, time.Local).UnixMilli()

	report := &ReportResponse{ParticipantReports: []ParticipantReport{
		{
			ParticipantID: "P1",
			Meters: []*MeterReport{{
				MeterID: "CONS1", From: from, Until: until,
				TimeWindows: []TimeWindow{
					{Key: "T2", From: "20:00", To: "06:00"},
					{Key: "T1", From: "06:00", To: "08:00"},
				},
			}},
		},
		{
			ParticipantID: "P2",
			Meters: []*MeterReport{{
				MeterID: "PROD1", From: from, Until: until,
				TimeWindows: []TimeWindow{
					{Key: "T1", From: "06:00", To: "08:00"},
				},
			}},
		},
	}}

	cpMap := map[string]*counterpoint.CounterPoint{
		"CONS1": {MeteringPoint: "CONS1", SourceIdx: 0, Direction: counterpoint.DirectionConsumer},
		"PROD1": {MeteringPoint: "PROD1", SourceIdx: 0, Direction: counterpoint.DirectionProducer},
	}

	cons := &participantConsumer{
		alloc:     AllocDynamicV2,
		report:    report,
		cpMap:     cpMap,
		startDate: time.Date(2024, 1, 1, 0, 0, 0, 0, time.Local),
		switchIdx: func(d time.Time) int { return d.Day() },
	}

	ctx := &queryengine.EngineContext{Info: &queryengine.CounterPointMetaInfo{ConsumerCount: 1, ProducerCount: 1}}
	if err := cons.HandleStart(ctx); err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		if err := cons.HandleLine(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	if err := cons.HandleEnd(ctx); err != nil {
		t.Fatal(err)
	}

	consumer := report.ParticipantReports[0].Meters[0]
	if consumer.Report == nil {
		t.Fatal("consumer report nil")
	}
	if got := consumer.Report.Summary.Utilization; got != 36.0 {
		t.Fatalf("consumer utilization = %v, want 36", got)
	}
	wantConsumer := []Bucket{{Key: "BASE", KWh: 5}, {Key: "T1", KWh: 5}, {Key: "T2", KWh: 26}}
	if len(consumer.Report.Buckets) != len(wantConsumer) {
		t.Fatalf("consumer buckets = %+v", consumer.Report.Buckets)
	}
	sum := 0.0
	for i, want := range wantConsumer {
		if consumer.Report.Buckets[i] != want {
			t.Errorf("consumer bucket[%d] = %+v, want %+v", i, consumer.Report.Buckets[i], want)
		}
		sum += consumer.Report.Buckets[i].KWh
	}
	// exact kWh partition: sum of buckets == period total
	if sum != consumer.Report.Summary.Utilization {
		t.Errorf("bucket sum %v != utilization %v", sum, consumer.Report.Summary.Utilization)
	}

	producer := report.ParticipantReports[1].Meters[0]
	if producer.Report == nil {
		t.Fatal("producer report nil")
	}
	if producer.Report.Summary.Production != 30.0 || producer.Report.Summary.Allocation != 9.0 {
		t.Fatalf("producer summary = %+v", producer.Report.Summary)
	}
	wantProducer := []Bucket{{Key: "BASE", KWh: 15}, {Key: "T1", KWh: 6}}
	if len(producer.Report.Buckets) != len(wantProducer) {
		t.Fatalf("producer buckets = %+v", producer.Report.Buckets)
	}
	for i, want := range wantProducer {
		if producer.Report.Buckets[i] != want {
			t.Errorf("producer bucket[%d] = %+v, want %+v", i, producer.Report.Buckets[i], want)
		}
	}
}

// TestParticipantConsumerNoWindows ensures a meter without time windows
// carries no buckets (unchanged behaviour).
func TestParticipantConsumerNoWindows(t *testing.T) {
	report := &ReportResponse{ParticipantReports: []ParticipantReport{{
		ParticipantID: "P1",
		Meters: []*MeterReport{{
			MeterID: "CONS1",
			From:    time.Date(2024, 1, 1, 0, 0, 0, 0, time.Local).UnixMilli(),
			Until:   time.Date(2024, 12, 31, 0, 0, 0, 0, time.Local).UnixMilli(),
		}},
	}}}
	cpMap := map[string]*counterpoint.CounterPoint{
		"CONS1": {MeteringPoint: "CONS1", SourceIdx: 0, Direction: counterpoint.DirectionConsumer},
	}
	cons := &participantConsumer{
		alloc:     AllocDynamicV2,
		report:    report,
		cpMap:     cpMap,
		startDate: time.Date(2024, 1, 1, 0, 0, 0, 0, time.Local),
		switchIdx: func(d time.Time) int { return d.Day() },
	}
	ctx := &queryengine.EngineContext{Info: &queryengine.CounterPointMetaInfo{ConsumerCount: 1, ProducerCount: 1}}
	if err := cons.HandleStart(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cons.HandleLine(ctx, &queryengine.RawSourceLine{
		ID: "CP/2024/01/01/12/00", Consumers: []float64{1, 0, 1}, Producers: []float64{0, 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cons.HandleEnd(ctx); err != nil {
		t.Fatal(err)
	}

	meter := report.ParticipantReports[0].Meters[0]
	if meter.Report == nil {
		t.Fatal("report nil")
	}
	if len(meter.Report.Buckets) != 0 {
		t.Fatalf("unexpected buckets: %+v", meter.Report.Buckets)
	}
	if meter.Report.Summary.Utilization != 1.0 {
		t.Fatalf("utilization = %v", meter.Report.Summary.Utilization)
	}
}
