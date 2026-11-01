package schedule

import (
	"reflect"
	"testing"
	"time"
)

func TestDaySlotsDSTAbsoluteInstants(t *testing.T) {
	for _, v := range []struct {
		date, zone string
		count      int
	}{
		{"2026-03-29", "Europe/Paris", 46}, {"2026-10-25", "Europe/Paris", 50},
		{"2026-09-06", "America/Santiago", 46}, {"2026-04-05", "Australia/Lord_Howe", 49},
		{"2026-10-04", "Australia/Lord_Howe", 47}, {"2030-01-01", "UTC", 48},
	} {
		cells, err := DaySlots(v.date, v.zone)
		if err != nil || len(cells) != v.count {
			t.Fatalf("%+v => %d %v", v, len(cells), err)
		}
		bounds, _ := DayBounds(v.date, v.zone)
		if !cells[0].Start.Equal(bounds.Start) || !cells[len(cells)-1].End.Equal(bounds.End) {
			t.Fatal("lost boundary")
		}
		for i, c := range cells {
			if c.End.Sub(c.Start) != 30*time.Minute || (i > 0 && !cells[i-1].End.Equal(c.Start)) {
				t.Fatalf("gap or duplicate %+v", c)
			}
		}
	}
	if _, err := DaySlots("2011-12-30", "Pacific/Apia"); err == nil {
		t.Fatal("accepted nonexistent day")
	}
}

func TestExplicitBlockedWinsAndUnknownElsewhere(t *testing.T) {
	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	w := func(a, b int) Interval {
		return Interval{base.Add(time.Duration(a) * time.Hour), base.Add(time.Duration(b) * time.Hour)}
	}
	members := []MemberAvailability{
		{UserID: 1, Windows: []Interval{w(0, 6)}}, {UserID: 2, Windows: []Interval{w(0, 6)}},
		// Includes a legacy/spillover want contradicted by a new explicit blocker.
		{UserID: 3, Windows: []Interval{w(0, 6)}, Blocked: []Interval{w(2, 3), w(3, 4)}},
		{UserID: 4, Blocked: []Interval{w(7, 8)}},
	}
	got := Suggestions(members, base, base.Add(6*time.Hour), time.Hour, 20)
	if len(got) != 2 {
		t.Fatalf("suggestions %+v", got)
	}
	for _, s := range got {
		if reflect.DeepEqual(s.Participants, []int64{1, 2, 3}) {
			if s.Start.Before(w(2, 4).End) && s.End.After(w(2, 4).Start) {
				t.Fatalf("blocked participant %+v", s)
			}
		} else if !reflect.DeepEqual(s.Participants, []int64{1, 2}) || !reflect.DeepEqual(s.Unavailable, []int64{3}) {
			t.Fatalf("missing explicit no classification %+v", s)
		}
		if !reflect.DeepEqual(s.Unknown, []int64{4}) {
			t.Fatalf("outside blocked became no %+v", s)
		}
	}
	// A range anchored inside the blocker returns a near match with explicit no.
	near := Suggestions(members, w(2, 4).Start, w(2, 4).End, time.Hour, 20)
	if len(near) != 1 || !reflect.DeepEqual(near[0].Unavailable, []int64{3}) {
		t.Fatalf("blocked classification %+v", near)
	}
	// Inputs must remain unmodified, including overlapping/adjacent blocker arrays.
	if !members[2].Windows[0].End.Equal(w(0, 6).End) {
		t.Fatal("mutated caller")
	}
	members[2].Windows = nil
	members[2].Blocked = nil
	got = Suggestions(members, base, base.Add(6*time.Hour), time.Hour, 20)
	if len(got) != 1 || !reflect.DeepEqual(got[0].Unknown, []int64{3, 4}) {
		t.Fatalf("blank is not unknown %+v", got)
	}
}

func TestSubtractDisjointNormalizedIntervals(t *testing.T) {
	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	w := func(a, b int) Interval {
		return Interval{base.Add(time.Duration(a) * time.Hour), base.Add(time.Duration(b) * time.Hour)}
	}
	for _, v := range []struct{ wants, blocked, want []Interval }{
		{[]Interval{w(1, 4)}, []Interval{w(0, 5)}, nil},
		{[]Interval{w(1, 4)}, []Interval{w(1, 2), w(3, 4)}, []Interval{w(2, 3)}},
		{[]Interval{w(0, 2), w(3, 5)}, []Interval{w(1, 4)}, []Interval{w(0, 1), w(4, 5)}},
		{[]Interval{w(0, 2)}, []Interval{w(2, 3)}, []Interval{w(0, 2)}},
	} {
		if got := subtract(v.wants, v.blocked); !reflect.DeepEqual(got, v.want) {
			t.Fatalf("%+v got %+v", v, got)
		}
	}
}

func TestPaintIntervalsPreservesOffGridEdges(t *testing.T) {
	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	w := func(a, b int) Interval {
		return Interval{base.Add(time.Duration(a) * time.Minute), base.Add(time.Duration(b) * time.Minute)}
	}
	wants := []Interval{w(13, 83), w(1440, 1500)}
	blocked := LegacyBlocked(w(0, 1440), wants)
	if !reflect.DeepEqual(blocked, []Interval{w(0, 13), w(83, 1440)}) {
		t.Fatalf("legacy complement %+v", blocked)
	}
	gotW, gotB := PaintIntervals(wants, blocked, w(30, 60), "blocked")
	if !reflect.DeepEqual(gotW, []Interval{w(13, 30), w(60, 83), w(1440, 1500)}) || !reflect.DeepEqual(gotB, []Interval{w(0, 13), w(30, 60), w(83, 1440)}) {
		t.Fatalf("paint %+v %+v", gotW, gotB)
	}
	gotW, gotB = PaintIntervals(gotW, gotB, w(30, 60), "erase")
	if len(gotB) != 2 || len(gotW) != 3 {
		t.Fatalf("erase %+v %+v", gotW, gotB)
	}
	gotW, gotB = PaintIntervals(gotW, gotB, w(30, 60), "want")
	if !reflect.DeepEqual(gotW, wants) || !reflect.DeepEqual(gotB, blocked) {
		t.Fatalf("want restore %+v %+v", gotW, gotB)
	}
	if !wants[0].Start.Equal(w(13, 83).Start) {
		t.Fatal("mutated originals")
	}
}
