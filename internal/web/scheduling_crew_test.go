package web

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"playpile/internal/schedule"
	"playpile/internal/store"
)

func crewTestMembers(n int) []store.Member {
	var out []store.Member
	for i := 0; i < n; i++ {
		out = append(out, store.Member{User: store.User{ID: int64(i + 1), DisplayName: fmt.Sprintf("Member %d", i+1)}})
	}
	return out
}
func crewTestInterval(start, end string) schedule.Interval {
	a, e := time.Parse(time.RFC3339, start)
	if e != nil {
		panic(e)
	}
	b, e := time.Parse(time.RFC3339, end)
	if e != nil {
		panic(e)
	}
	return schedule.Interval{Start: a, End: b}
}
func crewTestWeek(t *testing.T, date, zone string) []scheduleDayVM {
	t.Helper()
	day, e := scheduleTimetableDay(date, zone, 1, nil)
	if e != nil {
		t.Fatal(e)
	}
	return []scheduleDayVM{day}
}
func crewTestSlot(t *testing.T, week []scheduleDayVM, key string) scheduleSlotVM {
	t.Helper()
	for _, d := range week {
		for _, s := range d.Slots {
			if s.Start == key {
				return s
			}
		}
	}
	t.Fatalf("missing slot %s", key)
	return scheduleSlotVM{}
}
func TestScheduleCrewSparseStatesAndInputUnchanged(t *testing.T) {
	week := crewTestWeek(t, "2026-10-20", "UTC")
	saved := []store.AvailabilityDay{
		{UserID: 1, Date: "2026-10-20", Zone: "UTC", Explicit: true, Windows: []schedule.Interval{crewTestInterval("2026-10-20T18:00:00Z", "2026-10-20T22:00:00Z")}},
		{UserID: 2, Date: "2026-10-20", Zone: "UTC", Explicit: true,
			Windows: []schedule.Interval{crewTestInterval("2026-10-20T18:00:00Z", "2026-10-20T18:30:00Z"), crewTestInterval("2026-10-20T20:15:00Z", "2026-10-20T20:30:00Z")},
			Blocked: []schedule.Interval{crewTestInterval("2026-10-20T19:00:00Z", "2026-10-20T19:30:00Z"), crewTestInterval("2026-10-20T21:15:00Z", "2026-10-20T21:30:00Z")}},
	}
	before, _ := json.Marshal(saved)
	roster, e := scheduleCrewTimetable(week, crewTestMembers(3), 1, saved, "UTC")
	if e != nil {
		t.Fatal(e)
	}
	if len(roster) != 2 || roster[0].Name != "Member 2" || len(roster[0].Wants) != 2 || len(roster[0].Blocked) != 2 || len(roster[1].Wants) != 0 {
		t.Fatalf("roster %+v", roster)
	}
	for _, test := range []struct{ clock, states, bar string }{{"18:00", "wu", "want"}, {"19:00", "cu", "cant"}, {"20:00", "mu", "mixed"}, {"21:00", "mu", "mixed"}, {"22:00", "uu", "unknown"}} {
		s := crewTestSlot(t, week, "2026-10-20T"+test.clock+":00Z")
		if s.CrewStates != test.states || !reflect.DeepEqual(s.CrewBars, []string{test.bar, "unknown"}) || !strings.Contains(s.CrewSummary, "unknown") || !strings.Contains(s.CrewSummary, "partial/mixed") {
			t.Fatalf("%s: %+v", test.clock, s)
		}
	}
	after, _ := json.Marshal(saved)
	if string(before) != string(after) {
		t.Fatal("projection mutated source rows")
	}
}
func TestScheduleCrewLegacyOvernightAndBlockPrecedence(t *testing.T) {
	week := crewTestWeek(t, "2026-10-21", "UTC")
	saved := []store.AvailabilityDay{
		{UserID: 2, Date: "2026-10-20", Zone: "UTC", Windows: []schedule.Interval{crewTestInterval("2026-10-20T23:00:00Z", "2026-10-21T02:00:00Z")}},
		{UserID: 2, Date: "2026-10-21", Zone: "UTC"},
		{UserID: 2, Date: "2026-10-21", Zone: "UTC", Explicit: true, Blocked: []schedule.Interval{crewTestInterval("2026-10-21T01:00:00Z", "2026-10-21T01:30:00Z"), crewTestInterval("2026-10-21T00:45:00Z", "2026-10-21T01:00:00Z")}},
	}
	before, _ := json.Marshal(saved)
	roster, e := scheduleCrewTimetable(week, crewTestMembers(2), 1, saved, "UTC")
	if e != nil {
		t.Fatal(e)
	}
	for _, test := range []struct{ clock, state string }{{"00:00", "w"}, {"00:30", "m"}, {"01:00", "c"}, {"01:30", "w"}, {"02:00", "c"}} {
		if s := crewTestSlot(t, week, "2026-10-21T"+test.clock+":00Z"); s.CrewStates != test.state {
			t.Fatalf("%s got %s", test.clock, s.CrewStates)
		}
	}
	if !strings.Contains(roster[0].Wants[0], "21 Oct 2026, 00:00 +00:00") {
		t.Fatalf("tail not clipped: %+v", roster)
	}
	after, _ := json.Marshal(saved)
	if string(before) != string(after) {
		t.Fatal("legacy render mutated saved rows")
	}
}
func TestScheduleCrewOtherZoneCrossMidnightUnion(t *testing.T) {
	for _, tc := range []struct{ zone, date, start, mid, end, slot string }{
		{"Asia/Tokyo", "2026-10-21", "2026-10-20T23:00:00+09:00", "2026-10-21T00:00:00+09:00", "2026-10-21T00:30:00+09:00", "2026-10-20T15:00:00Z"},
		{"Asia/Kathmandu", "2026-10-21", "2026-10-20T23:30:00+05:45", "2026-10-21T00:00:00+05:45", "2026-10-21T00:30:00+05:45", "2026-10-20T18:00:00Z"},
		{"Asia/Kolkata", "2026-10-21", "2026-10-20T23:30:00+05:30", "2026-10-21T00:00:00+05:30", "2026-10-21T00:30:00+05:30", "2026-10-20T18:30:00Z"},
	} {
		t.Run(tc.zone, func(t *testing.T) {
			week := crewTestWeek(t, "2026-10-20", "UTC")
			rows := []store.AvailabilityDay{
				{UserID: 2, Date: "2026-10-20", Zone: tc.zone, Explicit: true, Windows: []schedule.Interval{crewTestInterval(tc.start, tc.mid)}},
				{UserID: 2, Date: tc.date, Zone: tc.zone, Explicit: true, Windows: []schedule.Interval{crewTestInterval(tc.mid, tc.end)}},
			}
			roster, e := scheduleCrewTimetable(week, crewTestMembers(2), 1, rows, "UTC")
			if e != nil {
				t.Fatal(e)
			}
			if s := crewTestSlot(t, week, tc.slot); s.CrewStates != "w" {
				t.Fatalf("source midnight split full want: %+v", s)
			}
			if len(roster[0].Wants) != 1 {
				t.Fatalf("source-date pieces not unioned: %+v", roster)
			}
		})
	}
}
func TestScheduleCrewRepeatedViewerDSTInstants(t *testing.T) {
	week := crewTestWeek(t, "2026-11-01", "America/New_York")
	rows := []store.AvailabilityDay{{UserID: 2, Date: "2026-11-01", Zone: "UTC", Explicit: true,
		Windows: []schedule.Interval{crewTestInterval("2026-11-01T05:00:00Z", "2026-11-01T05:30:00Z")},
		Blocked: []schedule.Interval{crewTestInterval("2026-11-01T06:00:00Z", "2026-11-01T06:30:00Z")}}}
	roster, e := scheduleCrewTimetable(week, crewTestMembers(2), 1, rows, "America/New_York")
	if e != nil {
		t.Fatal(e)
	}
	a, b := crewTestSlot(t, week, "2026-11-01T05:00:00Z"), crewTestSlot(t, week, "2026-11-01T06:00:00Z")
	if a.Time != b.Time || a.CrewStates != "w" || b.CrewStates != "c" || a.Label == b.Label {
		t.Fatalf("repeated clocks conflated: %+v %+v", a, b)
	}
	if !strings.Contains(roster[0].Wants[0], "-04:00") || !strings.Contains(roster[0].Blocked[0], "-05:00") {
		t.Fatalf("missing offsets %+v", roster)
	}
}
func TestScheduleCrewHundredMembersBoundedPreview(t *testing.T) {
	week := crewTestWeek(t, "2026-10-20", "UTC")
	members := crewTestMembers(101)
	roster, e := scheduleCrewTimetable(week, members, 1, nil, "UTC")
	if e != nil {
		t.Fatal(e)
	}
	if len(roster) != 100 {
		t.Fatal(len(roster))
	}
	for _, s := range week[0].Slots {
		if len(s.CrewStates) != 100 || len(s.CrewBars) != 4 || s.CrewMore != 96 || s.CrewStates != strings.Repeat("u", 100) {
			t.Fatalf("unbounded preview %+v", s)
		}
	}
	// No saved rows means no interval disclosure entries; output depends on
	// current roster and visible cells, not any retained application history.
	for _, m := range roster {
		if len(m.Wants)+len(m.Blocked) != 0 {
			t.Fatal("fabricated marks")
		}
	}
	first := week[0].Slots[0]
	if _, e := scheduleCrewTimetable(week, members, 1, nil, "UTC"); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(first, week[0].Slots[0]) {
		t.Fatal("re-render appended state/history")
	}
}

func TestScheduleCrewDisclosureRetainsLegacySeconds(t *testing.T) {
	week := crewTestWeek(t, "2026-10-20", "UTC")
	rows := []store.AvailabilityDay{{UserID: 2, Date: "2026-10-20", Zone: "UTC", Explicit: true,
		Windows: []schedule.Interval{crewTestInterval("2026-10-20T18:15:20Z", "2026-10-20T19:15:21Z")},
		Blocked: []schedule.Interval{crewTestInterval("2026-10-20T20:15:22Z", "2026-10-20T21:15:23Z")}}}
	roster, err := scheduleCrewTimetable(week, crewTestMembers(2), 1, rows, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(roster[0].Wants[0], "18:15:20 +00:00") || !strings.Contains(roster[0].Wants[0], "19:15:21 +00:00") || !strings.Contains(roster[0].Blocked[0], "20:15:22 +00:00") || !strings.Contains(roster[0].Blocked[0], "21:15:23 +00:00") {
		t.Fatalf("lost seconds %+v", roster)
	}
}

func TestScheduleCrewVisibleBoundsAndMalformedSource(t *testing.T) {
	week := crewTestWeek(t, "2026-10-20", "UTC")
	rows := []store.AvailabilityDay{{UserID: 2, Date: "2026-10-19", Zone: "UTC", Explicit: true,
		Windows: []schedule.Interval{crewTestInterval("2026-10-19T23:00:00Z", "2026-10-20T00:30:00Z")}},
		{UserID: 2, Date: "2026-10-20", Zone: "UTC", Explicit: true,
			Blocked: []schedule.Interval{crewTestInterval("2026-10-20T23:30:00Z", "2026-10-21T01:00:00Z")}}}
	roster, err := scheduleCrewTimetable(week, crewTestMembers(2), 1, rows, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if len(roster[0].Wants) != 1 || !strings.HasPrefix(roster[0].Wants[0], "Tue 20 Oct 2026, 00:00 +00:00") || !strings.HasSuffix(roster[0].Blocked[0], "Wed 21 Oct 2026, 00:00 +00:00") {
		t.Fatalf("unclipped disclosure %+v", roster)
	}
	if s := crewTestSlot(t, week, "2026-10-20T00:00:00Z"); s.CrewStates != "w" {
		t.Fatal("previous source tail lost")
	}
	rows[0].Zone = "Not/AZone"
	if _, err = scheduleCrewTimetable(week, crewTestMembers(2), 1, rows, "UTC"); err == nil {
		t.Fatal("invalid member zone accepted")
	}
	// An invalid unrelated member cannot poison this crew's projection.
	rows[0].UserID = 99
	if _, err = scheduleCrewTimetable(week, crewTestMembers(2), 1, rows, "UTC"); err != nil {
		t.Fatal(err)
	}
	roster, err = scheduleCrewTimetable(week, crewTestMembers(1), 1, nil, "UTC")
	if err != nil || len(roster) != 0 {
		t.Fatalf("solo crew %+v %v", roster, err)
	}
	for _, s := range week[0].Slots {
		if s.CrewStates != "" || len(s.CrewBars) != 0 || s.CrewMore != 0 {
			t.Fatalf("stale crew metadata %+v", s)
		}
	}
}

func TestScheduleCrewHTTPCurrentRosterOtherMarksAndReadOnly(t *testing.T) {
	f := newPlanningFixture(t)
	date := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	// Ada's own mark must not become another crew bar; Cy has no clock or answers.
	f.post(t, 0, "/slots", slotEditsForm(t, scheduleSlotEdit{Date: date, Changes: []scheduleSlotChange{slotChange(date, "17:00", "want")}}), 303)
	f.post(t, 1, "/slots", slotEditsForm(t, scheduleSlotEdit{Date: date, Changes: []scheduleSlotChange{slotChange(date, "18:00", "want"), slotChange(date, "19:00", "cant")}}), 303)
	other, e := f.st.CreateCrew("Elsewhere", "US", "english", f.users[3].ID)
	if e != nil {
		t.Fatal(e)
	}
	w, _ := schedule.LocalWindow(date, "UTC", "18:00", "20:00")
	if e = f.st.SaveAvailabilitySlotsBatch(other.ID, f.users[3].ID, "UTC", []store.AvailabilitySlotDay{{Date: date, Wants: []schedule.Interval{w}}}); e != nil {
		t.Fatal(e)
	}
	bounds, _ := schedule.DayBounds(date, "UTC")
	before, e := f.st.AvailabilityDays(f.crew.ID, f.users[0].ID, bounds.Start, bounds.End)
	if e != nil {
		t.Fatal(e)
	}
	resp, body := do(t, f.clients[0], "GET", f.target+"?tz=UTC", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET %d", resp.StatusCode)
	}
	// Inspect each actual cell instead of accidentally matching another day.
	for _, tc := range []struct{ clock, states string }{{"17:00", "uu"}, {"18:00", "wu"}, {"19:00", "cu"}} {
		start := strings.Index(body, `data-crew-slot="`+date+`T`+tc.clock+`:00Z"`)
		if start < 0 {
			t.Fatalf("missing HTTP cell %s", tc.clock)
		}
		tag := strings.SplitN(body[start:], ">", 2)[0]
		if !strings.Contains(tag, `data-crew-states="`+tc.states+`"`) {
			t.Fatalf("HTTP crew projection %s: %s", tc.clock, tag)
		}
	}
	if strings.Contains(body, "Outsider") {
		t.Fatal("cross-crew roster leaked")
	}
	after, e := f.st.AvailabilityDays(f.crew.ID, f.users[0].ID, bounds.Start, bounds.End)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("HTTP render changed stored rows")
	}
	resp, _ = do(t, f.clients[3], "GET", f.target, nil, nil)
	if resp.StatusCode != 403 {
		t.Fatal("outsider could read crew")
	}
}
