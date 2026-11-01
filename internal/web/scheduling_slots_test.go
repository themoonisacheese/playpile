package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"playpile/internal/schedule"
	"playpile/internal/store"
)

func slotEditsForm(t *testing.T, edits ...scheduleSlotEdit) url.Values {
	t.Helper()
	data, err := json.Marshal(edits)
	if err != nil {
		t.Fatal(err)
	}
	return url.Values{"edits": {string(data)}}
}
func slotChange(date, clock, state string) scheduleSlotChange {
	return scheduleSlotChange{Start: date + "T" + clock + ":00Z", State: state}
}
func ownPlanningDays(t *testing.T, f planningFixture) []store.AvailabilityDay {
	t.Helper()
	now := time.Now().UTC()
	bounds, _ := schedule.DayBounds(now.Format("2006-01-02"), "UTC")
	rows, err := f.st.AvailabilityDays(f.crew.ID, f.users[0].ID, bounds.Start, bounds.Start.AddDate(0, 0, 28))
	if err != nil {
		t.Fatal(err)
	}
	var own []store.AvailabilityDay
	for _, row := range rows {
		if row.UserID == f.users[0].ID {
			own = append(own, row)
		}
	}
	return own
}
func TestTimetableSlotsHTTPWantBlockEraseAndBlank(t *testing.T) {
	f := newPlanningFixture(t)
	date := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	edit := scheduleSlotEdit{Date: date, Changes: []scheduleSlotChange{slotChange(date, "18:00", "want"), slotChange(date, "19:00", "cant")}}
	form := slotEditsForm(t, edit)
	form.Set("user_id", fmt.Sprint(f.users[1].ID))
	f.post(t, 0, "/slots", form, 303)
	days := ownPlanningDays(t, f)
	if len(days) != 1 || !days[0].Explicit || len(days[0].Windows) != 1 || len(days[0].Blocked) != 1 {
		t.Fatalf("want/cant not persisted: %+v", days)
	}
	if days[0].Windows[0].End.Sub(days[0].Windows[0].Start) != 30*time.Minute {
		t.Fatal("blank implicitly filled")
	}
	_, body := do(t, f.clients[0], "GET", f.target, nil, nil)
	for _, fragment := range []string{`data-slot="` + date + `T18:00:00Z" data-state="want"`, `data-slot="` + date + `T19:00:00Z" data-state="cant"`, `data-slot="` + date + `T20:00:00Z" data-state="unknown"`} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("missing prefill %s", fragment)
		}
	}
	// Native/no-JavaScript slot form only changes requested slots.
	f.post(t, 0, "/slots", url.Values{"date": {date}, "slot:" + date + "T18:00:00Z": {"unknown"}}, 303)
	days = ownPlanningDays(t, f)
	if len(days) != 1 || len(days[0].Windows) != 0 || len(days[0].Blocked) != 1 {
		t.Fatal("erase changed untouched block")
	}
	f.post(t, 0, "/slots", slotEditsForm(t, scheduleSlotEdit{Date: date, Changes: []scheduleSlotChange{slotChange(date, "19:00", "unknown")}}), 303)
	if days = ownPlanningDays(t, f); len(days) != 0 {
		t.Fatal("all erased should be unanswered, not whole-day no")
	}
	// Clearing the last painted day releases timezone lock.
	resp, _ := do(t, f.clients[0], "GET", f.target+"?tz=Asia%2FTokyo", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatal("erased day kept timezone locked")
	}
}
func TestTimetableSlotBatchAtomicAndInputSecurity(t *testing.T) {
	f := newPlanningFixture(t)
	date := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	next := time.Now().UTC().AddDate(0, 0, 2).Format("2006-01-02")
	valid := scheduleSlotEdit{Date: date, Changes: []scheduleSlotChange{slotChange(date, "18:00", "want")}}
	for _, bad := range []scheduleSlotEdit{
		{Date: next, Changes: []scheduleSlotChange{slotChange(next, "18:01", "want")}},
		{Date: next, Changes: []scheduleSlotChange{slotChange(next, "18:00", "bogus")}},
		{Date: date, Changes: []scheduleSlotChange{slotChange(date, "19:00", "cant")}},
		{Date: next, Changes: []scheduleSlotChange{slotChange(date, "19:00", "cant")}},
		{Date: next, Changes: []scheduleSlotChange{slotChange(next, "19:00", "cant"), slotChange(next, "19:00", "want")}},
	} {
		f.post(t, 0, "/slots", slotEditsForm(t, valid, bad), 400)
		if len(ownPlanningDays(t, f)) != 0 {
			t.Fatal("invalid second day partially wrote batch")
		}
	}
	f.post(t, 0, "/slots", url.Values{"edits": {"[] trailing"}}, 400)
	resp, _ := do(t, f.clients[0], "POST", f.target+"/slots", slotEditsForm(t, valid), nil)
	if resp.StatusCode != 403 {
		t.Fatal("CSRF bypass")
	}
	resp, _ = do(t, f.clients[0], "POST", f.target+"/slots", nil, map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != 403 {
		t.Fatal("JSON CSRF bypass")
	}
	f.post(t, 3, "/slots", slotEditsForm(t, valid), 403)
	f.post(t, 0, "/slots", url.Values{"padding": {strings.Repeat("x", 129<<10)}}, 413)
	if len(ownPlanningDays(t, f)) != 0 {
		t.Fatal("oversize or unauthorized write changed availability")
	}
	other, err := f.st.CreateCrew("Elsewhere", "US", "english", f.users[3].ID)
	if err != nil {
		t.Fatal(err)
	}
	form := slotEditsForm(t, valid)
	form.Set("csrf", f.tokens[0])
	form.Set("tz", "UTC")
	resp, _ = do(t, f.clients[0], "POST", fmt.Sprintf("%s/c/%d/schedule/slots", f.ts.URL, other.ID), form, nil)
	if resp.StatusCode != 403 {
		t.Fatal("cross-crew slot write")
	}
}
func TestTimetableNativeValidationPreservesDraft(t *testing.T) {
	f := newPlanningFixture(t)
	date := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	body := f.post(t, 0, "/slots", url.Values{"date": {date}, "slot:" + date + "T18:00:00Z": {"cant"}, "slot:" + date + "T19:01:00Z": {"want"}}, 400)
	expected := `name="slot:` + date + `T18:00:00Z"><option value="" >Keep saved answer</option><option value="want" >✓ Want to play</option><option value="cant" selected>`
	if !strings.Contains(body, expected) {
		t.Fatal("invalid native save discarded painted choice")
	}
	if len(ownPlanningDays(t, f)) != 0 {
		t.Fatal("invalid native save partially wrote")
	}
}

func TestTimetableMaximumAlternatingWeekAndAjax(t *testing.T) {
	f := newPlanningFixture(t)
	var edits []scheduleSlotEdit
	for i := 0; i < 7; i++ {
		date := time.Now().UTC().AddDate(0, 0, i).Format("2006-01-02")
		cells, _ := schedule.DaySlots(date, "UTC")
		edit := scheduleSlotEdit{Date: date}
		for j, cell := range cells {
			state := "want"
			if j%2 == 1 {
				state = "cant"
			}
			edit.Changes = append(edit.Changes, scheduleSlotChange{Start: cell.Start.UTC().Format(time.RFC3339), State: state})
		}
		edits = append(edits, edit)
	}
	form := slotEditsForm(t, edits...)
	form.Set("csrf", f.tokens[0])
	form.Set("tz", "UTC")
	// Changed-cell encoding is compact; also verify the slot-specific body budget.
	form.Set("padding", strings.Repeat("x", 33<<10))
	resp, body := do(t, f.clients[0], "POST", f.target+"/slots", form, map[string]string{"Accept": "application/json"})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"saved":true`) {
		t.Fatalf("maximum week save %d %s", resp.StatusCode, body)
	}
	if len(ownPlanningDays(t, f)) != 7 {
		t.Fatal("maximum week incomplete")
	}
}
func TestTimetableLegacyPrecisionSpillAndOffscreenPreserved(t *testing.T) {
	f := newPlanningFixture(t)
	now := time.Now().UTC()
	date := now.AddDate(0, 0, 1).Format("2006-01-02")
	next := now.AddDate(0, 0, 2).Format("2006-01-02")
	offscreen := now.AddDate(0, 0, 10).Format("2006-01-02")
	partial, _ := schedule.LocalWindow(date, "UTC", "18:15", "19:15")
	overnight, _ := schedule.LocalWindow(date, "UTC", "22:00", "02:00")
	far, _ := schedule.LocalWindow(offscreen, "UTC", "18:15", "20:15")
	if err := f.st.SaveAvailabilityDay(f.crew.ID, f.users[0].ID, date, "UTC", []schedule.Interval{partial, overnight}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SaveAvailabilityDay(f.crew.ID, f.users[0].ID, offscreen, "UTC", []schedule.Interval{far}); err != nil {
		t.Fatal(err)
	}
	_, body := do(t, f.clients[0], "GET", f.target, nil, nil)
	if !strings.Contains(body, `data-slot="`+date+`T18:00:00Z" data-state="mixed"`) || !strings.Contains(body, `data-slot="`+next+`T01:00:00Z" data-state="want"`) {
		t.Fatal("legacy partial and overnight prefills lost")
	}
	f.post(t, 0, "/slots", slotEditsForm(t, scheduleSlotEdit{Date: next, Changes: []scheduleSlotChange{slotChange(next, "01:00", "unknown")}}, scheduleSlotEdit{Date: date, Changes: []scheduleSlotChange{slotChange(date, "20:00", "want")}}), 303)
	days := ownPlanningDays(t, f)
	var foundPartial, foundFar bool
	for _, d := range days {
		for _, w := range d.Windows {
			if w.Start.Equal(partial.Start) && w.End.Equal(partial.End) {
				foundPartial = true
			}
			if w.Start.Equal(far.Start) && w.End.Equal(far.End) {
				foundFar = true
			}
			erased, _ := time.Parse(time.RFC3339, next+"T01:15:00Z")
			if !erased.Before(w.Start) && erased.Before(w.End) {
				t.Fatal("hidden legacy tail resurrected erased cell")
			}
		}
	}
	if !foundPartial || !foundFar {
		t.Fatal("untouched exact minutes or offscreen window changed")
	}
}
func TestTimetableDSTAbsoluteRowsAndTimezone(t *testing.T) {
	for _, tc := range []struct {
		date  string
		count int
	}{{"2030-03-10", 46}, {"2030-11-03", 50}} {
		day, err := scheduleTimetableDay(tc.date, "America/New_York", 1, nil)
		if err != nil || len(day.Slots) != tc.count {
			t.Fatalf("DST rows %s: %d %v", tc.date, len(day.Slots), err)
		}
		seen := map[string]bool{}
		for _, slot := range day.Slots {
			if seen[slot.Start] {
				t.Fatal("repeated hour keys collide")
			}
			seen[slot.Start] = true
		}
		if tc.count == 50 {
			var labels []string
			for _, slot := range day.Slots {
				if slot.Time == "01:30" {
					labels = append(labels, slot.Label)
				}
			}
			if len(labels) != 2 || labels[0] == labels[1] {
				t.Fatal("repeated hour not offset distinguished")
			}
		}
	}
	f := newPlanningFixture(t)
	loc, _ := time.LoadLocation("Asia/Tokyo")
	date := time.Now().In(loc).AddDate(0, 0, 1).Format("2006-01-02")
	cells, _ := schedule.DaySlots(date, loc.String())
	form := slotEditsForm(t, scheduleSlotEdit{Date: date, Changes: []scheduleSlotChange{{Start: cells[36].Start.UTC().Format(time.RFC3339), State: "want"}}})
	form.Set("tz", loc.String())
	f.post(t, 0, "/slots", form, 303)
	days := ownPlanningDays(t, f)
	if len(days) != 1 || days[0].Windows[0].Start.UTC().Hour() != 9 {
		t.Fatal("timezone slot instant changed")
	}
	// Another valid zone must not reinterpret those saved absolute instants.
	wrong := slotEditsForm(t, scheduleSlotEdit{Date: date, Changes: []scheduleSlotChange{slotChange(date, "18:00", "want")}})
	f.post(t, 0, "/slots", wrong, 400)
}
