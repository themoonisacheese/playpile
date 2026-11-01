package schedule

import (
	"reflect"
	"testing"
	"time"
)

func TestNormalizedCoverageBinarySearch(t *testing.T) {
	origin := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	at := func(h int) time.Time { return origin.Add(time.Duration(h) * time.Hour) }
	raw := []Interval{{at(3), at(4)}, {at(0), at(1)}, {at(1), at(2)}}
	before := append([]Interval(nil), raw...)
	normalized := normalize(raw, at(0), at(5))
	for _, tc := range []struct {
		a, b int
		want bool
	}{{0, 2, true}, {1, 2, true}, {3, 4, true}, {2, 3, false}, {1, 3, false}, {4, 5, false}, {0, 4, false}} {
		_, got := covering(normalized, at(tc.a), at(tc.b))
		if got != tc.want {
			t.Fatalf("coverage %d..%d=%v want%v", tc.a, tc.b, got, tc.want)
		}
	}
	if !reflect.DeepEqual(raw, before) {
		t.Fatal("normalization mutated input")
	}
}

func TestSuggestionsMapOrderStable(t *testing.T) {
	origin := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	w := func(a, b int) Interval {
		return Interval{origin.Add(time.Duration(a) * time.Hour), origin.Add(time.Duration(b) * time.Hour)}
	}
	members := []MemberAvailability{
		{UserID: 4, Windows: []Interval{w(3, 6)}, Reviewed: []Interval{w(0, 8)}},
		{UserID: 2, Windows: []Interval{w(0, 3), w(4, 8)}},
		{UserID: 3, Windows: []Interval{w(1, 7)}},
		{UserID: 1, Windows: []Interval{w(0, 8)}},
	}
	want := Suggestions(members, origin, origin.Add(8*time.Hour), time.Hour, 24)
	for i := 0; i < 100; i++ {
		got := Suggestions(members, origin, origin.Add(8*time.Hour), time.Hour, 24)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("nondeterministic order: got %+v want %+v", got, want)
		}
	}
}
