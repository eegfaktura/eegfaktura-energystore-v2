package queryengine

import (
	"reflect"
	"testing"
)

// Regression for the "/query/rawdata returns each timestamp N times" bug
// (support cases RC101586 / RC105720), same root cause as v1: a metering point
// re-registered under a new member, with the old overlapping participant row
// still listed, appears in the request `cps` N times. The store holds exactly
// one series per metering-point name, so every duplicate cps entry produced one
// identical extra data series (ParentFunction.addToResult). QueryRawData now
// de-duplicates the target list before querying.
func TestDedupTargets(t *testing.T) {
	zp := "AT0030000000000000000000000363736"
	other := "AT0031000000000000000000099022000"

	cases := []struct {
		name string
		in   []TargetMP
		want []TargetMP
	}{
		{
			// the re-registered ZP appears twice (old INACTIVE + new ACTIVE row)
			name: "collapses duplicate ZP, order preserved",
			in:   []TargetMP{{MeteringPoint: zp}, {MeteringPoint: other}, {MeteringPoint: zp}},
			want: []TargetMP{{MeteringPoint: zp}, {MeteringPoint: other}},
		},
		{
			name: "four duplicates (original report) collapse to one",
			in:   []TargetMP{{MeteringPoint: zp}, {MeteringPoint: zp}, {MeteringPoint: zp}, {MeteringPoint: zp}},
			want: []TargetMP{{MeteringPoint: zp}},
		},
		{
			name: "list without duplicates is unchanged",
			in:   []TargetMP{{MeteringPoint: zp}, {MeteringPoint: other}},
			want: []TargetMP{{MeteringPoint: zp}, {MeteringPoint: other}},
		},
		{
			name: "empty stays empty",
			in:   []TargetMP{},
			want: []TargetMP{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dedupTargets(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("dedupTargets(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
