package calc

import (
	"fmt"
)

// Time-of-use window folding (ZVT), parity port of v1
// calculation/timeWindows.go.
//
// Timezone note (B4): the queryengine renders row ids from the stored
// timestamptz in the container's local time (Dockerfile pins
// TZ=Europe/Vienna), so window membership compares directly against the
// wall-clock HH:MM encoded in the row id - no further conversion. DST: on
// the 23h day a window quarter-hour occurs once less, on the 25h day twice
// (v2 keeps both instants, unlike v1's key collision).

// timeWindowRange is a parsed window; from inclusive, to exclusive, minutes
// since midnight. from > to means the window crosses midnight.
type timeWindowRange struct {
	rangeKey string // "HH:MM-HH:MM", used to de-duplicate identical ranges
	fromMin  int
	toMin    int
}

func parseWindowTime(s string) (int, error) {
	var hh, mm int
	if _, err := fmt.Sscanf(s, "%d:%d", &hh, &mm); err != nil {
		return 0, fmt.Errorf("invalid time %q (expected HH:MM)", s)
	}
	if hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("invalid time %q (expected HH:MM)", s)
	}
	if mm%15 != 0 {
		return 0, fmt.Errorf("time %q not on 15-min raster (00/15/30/45)", s)
	}
	return hh*60 + mm, nil
}

func parseTimeWindow(tw TimeWindow) (timeWindowRange, error) {
	if tw.Key != "T1" && tw.Key != "T2" {
		return timeWindowRange{}, fmt.Errorf("invalid time window key %q (expected T1 or T2)", tw.Key)
	}
	fromMin, err := parseWindowTime(tw.From)
	if err != nil {
		return timeWindowRange{}, err
	}
	toMin, err := parseWindowTime(tw.To)
	if err != nil {
		return timeWindowRange{}, err
	}
	if fromMin == toMin {
		return timeWindowRange{}, fmt.Errorf("time window %s: from == to (%s) is not allowed", tw.Key, tw.From)
	}
	return timeWindowRange{rangeKey: tw.From + "-" + tw.To, fromMin: fromMin, toMin: toMin}, nil
}

// contains reports whether the quarter-hour starting at minute-of-day m lies
// inside the window ([from, to), cyclic over midnight when from > to).
func (w timeWindowRange) contains(m int) bool {
	if w.fromMin < w.toMin {
		return m >= w.fromMin && m < w.toMin
	}
	return m >= w.fromMin || m < w.toMin
}

// ValidateTimeWindows checks all time windows of a report request:
// max 2 per meter, unique keys T1/T2, HH:MM on the 15-min raster, from != to.
func ValidateTimeWindows(participants []ParticipantReport) error {
	for _, p := range participants {
		for _, m := range p.Meters {
			if len(m.TimeWindows) > 2 {
				return fmt.Errorf("meter %s: more than 2 time windows", m.MeterID)
			}
			seen := map[string]bool{}
			for _, tw := range m.TimeWindows {
				if seen[tw.Key] {
					return fmt.Errorf("meter %s: duplicate time window key %s", m.MeterID, tw.Key)
				}
				seen[tw.Key] = true
				if _, err := parseTimeWindow(tw); err != nil {
					return fmt.Errorf("meter %s: %w", m.MeterID, err)
				}
			}
		}
	}
	return nil
}

// distinctWindowRanges collects the de-duplicated window ranges of all
// requested meters (different tariffs may define different windows).
func distinctWindowRanges(report *ReportResponse) []timeWindowRange {
	var ranges []timeWindowRange
	seen := map[string]bool{}
	for prIdx := range report.ParticipantReports {
		for _, m := range report.ParticipantReports[prIdx].Meters {
			for _, tw := range m.TimeWindows {
				w, err := parseTimeWindow(tw)
				if err != nil {
					continue // rejected upstream by ValidateTimeWindows
				}
				if !seen[w.rangeKey] {
					seen[w.rangeKey] = true
					ranges = append(ranges, w)
				}
			}
		}
	}
	return ranges
}
