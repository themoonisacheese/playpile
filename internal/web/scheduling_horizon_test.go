package web

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSchedulingVisibleDateHorizonAndCleanup(t *testing.T) {
	f := newPlanningFixture(t)
	now := time.Now().UTC()
	minDate, maxDate := scheduleDateRange(now, time.UTC)
	outside := now.AddDate(0, 0, 7).Format("2006-01-02")
	resp, body := do(t, f.clients[0], "GET", f.target, nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, `data-min="`+minDate+`" data-max="`+maxDate+`"`) {
		t.Fatal("editor did not expose exact seven-date bounds")
	}
	resp, body = do(t, f.clients[0], "GET", f.target+"?date="+outside, nil, nil)
	if resp.StatusCode != 400 || !strings.Contains(body, "7-day planning view") {
		t.Fatal("offscreen selected date must be rejected with actionable dates")
	}
	form := url.Values{"date": {outside}, "status": {"available"}, "start": {"18:00"}, "end": {"20:00"}}
	body = f.post(t, 0, "/availability", form, 400)
	if !strings.Contains(body, "7-day planning view") || !strings.Contains(body, `value="`+outside+`"`) {
		t.Fatal("out-of-range form must preserve input and explain visible horizon")
	}
	queryStart := time.Now()
	days, err := f.st.AvailabilityDays(f.crew.ID, f.users[0].ID, queryStart, queryStart.Add(28*24*time.Hour))
	if err != nil || len(days) != 0 {
		t.Fatal("rejected offscreen save wrote availability")
	}
	// Store's longer foundation horizon stays intact; a preexisting offscreen
	// record cannot be overwritten by this UI, but can explicitly be cleared.
	if err := f.st.SaveAvailabilityDay(f.crew.ID, f.users[0].ID, outside, "UTC", nil); err != nil {
		t.Fatal(err)
	}
	f.post(t, 0, "/availability", form, 400)
	days, err = f.st.AvailabilityDays(f.crew.ID, f.users[0].ID, queryStart, queryStart.Add(28*24*time.Hour))
	if err != nil || len(days) != 1 || len(days[0].Windows) != 0 {
		t.Fatal("rejected replacement modified preexisting offscreen record")
	}
	f.post(t, 0, "/availability", url.Values{"date": {outside}, "status": {"unknown"}}, 303)
	days, err = f.st.AvailabilityDays(f.crew.ID, f.users[0].ID, queryStart, queryStart.Add(28*24*time.Hour))
	if err != nil || len(days) != 0 {
		t.Fatal("explicit cleanup did not remove offscreen answer")
	}
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	f.post(t, 0, "/availability", url.Values{"date": {yesterday}, "status": {"unknown"}}, 303)
}

func TestScheduleDateRangeUsesLocalDates(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	first, last := scheduleDateRange(time.Date(2030, 1, 2, 20, 0, 0, 0, time.UTC), loc)
	if first != "2030-01-03" || last != "2030-01-09" {
		t.Fatalf("wrong local bounds %s–%s", first, last)
	}
	if err := scheduleValidateDate("2030-01-10", time.Date(2030, 1, 2, 20, 0, 0, 0, time.UTC), loc); err == nil {
		t.Fatal("8th local date accepted")
	}
}
