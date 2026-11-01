package schedule

import (
	"reflect"
	"testing"
	"time"
)

func TestCivilWindows(t *testing.T) {
	cases := []struct {
		date, zone, a, b string
		duration         time.Duration
	}{
		{"2030-01-01", "UTC", "23:00", "02:00", 3 * time.Hour},
		{"2026-03-29", "Europe/Paris", "01:00", "04:00", 2 * time.Hour},
		{"2026-10-25", "Europe/Paris", "01:00", "04:00", 4 * time.Hour},
		{"2030-01-01", "Asia/Kathmandu", "22:00", "23:00", time.Hour},
	}
	for _, c := range cases {
		w, err := LocalWindow(c.date, c.zone, c.a, c.b)
		if err != nil || w.End.Sub(w.Start) != c.duration {
			t.Fatalf("%+v: %+v %v", c, w, err)
		}
	}
	for _, c := range []struct{ date, zone, a, b string }{
		{"2026-03-29", "Europe/Paris", "02:30", "04:00"},
		{"2026-10-25", "Europe/Paris", "02:30", "04:00"},
		{"2030-01-01", "UTC", "10:00", "10:00"},
		{"2030-01-01", "Local", "10:00", "11:00"},
		{"2030-01-01", "UTC", "25:00", "11:00"},
	} {
		if _, err := LocalWindow(c.date, c.zone, c.a, c.b); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	a, _ := LocalTime("2030-01-01", "Asia/Tokyo", "19:00")
	b, _ := LocalTime("2030-01-01", "UTC", "10:00")
	if !a.Equal(b) {
		t.Fatal("zones fail")
	}
}
func TestDayBoundsTransitions(t *testing.T) {
	for _, c := range []struct {
		date, zone string
		hours      int
	}{
		{"2026-03-29", "Europe/Paris", 23}, {"2026-10-25", "Europe/Paris", 25}, {"2026-09-06", "America/Santiago", 23},
	} {
		w, err := DayBounds(c.date, c.zone)
		if err != nil || w.End.Sub(w.Start) != time.Duration(c.hours)*time.Hour {
			t.Fatalf("%+v: %+v %v", c, w, err)
		}
		loc, _ := time.LoadLocation(c.zone)
		if w.Start.In(loc).Format("2006-01-02") != c.date {
			t.Fatalf("wrong start %+v", w)
		}
	}
	if _, err := DayBounds("2011-12-30", "Pacific/Apia"); err == nil {
		t.Fatal("accepted skipped date")
	}
}
func TestSuggestionsAdjacencyUnknownAndIdentity(t *testing.T) {
	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	w := func(a, b int) Interval {
		return Interval{base.Add(time.Duration(a) * time.Hour), base.Add(time.Duration(b) * time.Hour)}
	}
	members := []MemberAvailability{{UserID: 1, Windows: []Interval{w(0, 1), w(1, 3)}, Reviewed: []Interval{w(0, 5)}}, {UserID: 2, Windows: []Interval{w(0, 3)}, Reviewed: []Interval{w(0, 5)}}, {UserID: 3, Reviewed: []Interval{w(0, 1)}}}
	got := Suggestions(members, base, base.Add(5*time.Hour), time.Hour, 20)
	if len(got) != 1 || !reflect.DeepEqual(got[0].Participants, []int64{1, 2}) || !reflect.DeepEqual(got[0].Unavailable, []int64{3}) || got[0].End != base.Add(3*time.Hour) {
		t.Fatalf("classification/adjacency %+v", got)
	}
	members[2].Reviewed = nil
	got = Suggestions(members, base, base.Add(5*time.Hour), time.Hour, 20)
	if len(got) != 1 || !reflect.DeepEqual(got[0].Unknown, []int64{3}) {
		t.Fatalf("unknown conflated %+v", got)
	}
	members[2].Windows = []Interval{w(1, 2)}
	got = Suggestions(members, base, base.Add(5*time.Hour), time.Hour, 20)
	if len(got) != 2 || !got[0].FullCrew || got[0].Start != base.Add(time.Hour) {
		t.Fatalf("full first/group boundaries %+v", got)
	}
	if got := Suggestions(members, base, base.Add(5*time.Hour), 4*time.Hour, 20); len(got) != 0 {
		t.Fatalf("min ignored %+v", got)
	}
}
func TestContinuousSubgroupsAndBackExtension(t *testing.T) {
	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	w := func(a, b int) Interval {
		return Interval{base.Add(time.Duration(a) * time.Hour), base.Add(time.Duration(b) * time.Hour)}
	}
	members := []MemberAvailability{{UserID: 1, Windows: []Interval{w(0, 4)}}, {UserID: 2, Windows: []Interval{w(0, 3)}}, {UserID: 3, Windows: []Interval{w(1, 4)}}}
	got := Suggestions(members, base, base.Add(5*time.Hour), 2*time.Hour, 20)
	if len(got) != 3 || !got[0].FullCrew {
		t.Fatalf("missing overlapping groups %+v", got)
	}
	for _, want := range []struct {
		ids  []int64
		a, b int
	}{{[]int64{1, 2, 3}, 1, 3}, {[]int64{1, 2}, 0, 3}, {[]int64{1, 3}, 1, 4}} {
		found := false
		for _, s := range got {
			if reflect.DeepEqual(s.Participants, want.ids) && s.Start.Equal(w(want.a, want.b).Start) && s.End.Equal(w(want.a, want.b).End) {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %+v in %+v", want, got)
		}
	}
	members[0].Windows = []Interval{w(0, 3)}
	members[1].Windows = []Interval{w(0, 3)}
	members[2].Windows = []Interval{w(1, 2)}
	members[2].Reviewed = []Interval{w(0, 5)}
	got = Suggestions(members, base, base.Add(5*time.Hour), 2*time.Hour, 20)
	if len(got) != 1 || !reflect.DeepEqual(got[0].Participants, []int64{1, 2}) || !reflect.DeepEqual(got[0].Unavailable, []int64{3}) || got[0].End != base.Add(3*time.Hour) {
		t.Fatalf("fragmented useful subgroup %+v", got)
	}
}
