package web

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSuggestedProposalPreservesDSTFoldInstant(t *testing.T) {
	const start = "2030-11-03T01:30"
	const end = "2030-11-03T04:30"
	if _, _, err := scheduleProposalTimes(start, end, "America/New_York"); err == nil {
		t.Fatal("manual ambiguous start accepted")
	}
	a, b, err := scheduleProposalTimesWithHints(start, end, "America/New_York", "2030-11-03T06:30:00Z", "2030-11-03T09:30:00Z")
	if err != nil || a.UTC().Format(time.RFC3339) != "2030-11-03T06:30:00Z" || b.Sub(a) != 3*time.Hour {
		t.Fatalf("exact suggested fold instant lost: %v %v %v", a, b, err)
	}
	if _, _, err := scheduleProposalTimesWithHints("2030-11-03T01:45", end, "America/New_York", "2030-11-03T06:30:00Z", "2030-11-03T09:30:00Z"); err == nil {
		t.Fatal("edited ambiguous input reused stale hint")
	}
	a, _, err = scheduleProposalTimesWithHints("2030-11-03T03:00", end, "America/New_York", "2030-11-03T06:30:00Z", "2030-11-03T09:30:00Z")
	if err != nil || a.UTC().Hour() != 8 {
		t.Fatal("edited unambiguous input must use strict local parser")
	}
	if _, _, err := scheduleProposalTimesWithHints(start, end, "America/New_York", "2030-11-03T06:31:00Z", "2030-11-03T09:30:00Z"); err == nil {
		t.Fatal("mismatched hint bypassed fold rejection")
	}
}
func TestSuggestedProposalHintsHTTP(t *testing.T) {
	f := newPlanningFixture(t)
	date := time.Now().UTC().AddDate(0, 0, 2).Format("2006-01-02")
	values := url.Values{"tz": {"UTC"}, "proposal_start": {date + "T18:00"}, "proposal_end": {date + "T19:00"}, "proposal_start_instant": {date + "T18:00:00Z"}, "proposal_end_instant": {date + "T19:00:00Z"}}
	resp, body := do(t, f.clients[0], "GET", f.target+"?"+values.Encode(), nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, `name="proposal_start_instant" value="`+date+`T18:00:00Z"`) || !strings.Contains(body, "Selected window:") {
		t.Fatal("selected exact instants/offsets not rendered")
	}
	values.Set("title", strings.Repeat("a", 161))
	body = f.post(t, 0, "/proposals", values, 400)
	if !strings.Contains(body, `name="proposal_start_instant" value="`+date+`T18:00:00Z"`) {
		t.Fatal("validation error discarded exact instant hint")
	}
	values.Set("title", "Selected exact time")
	f.post(t, 0, "/proposals", values, 303)
	proposals, err := f.st.Proposals(f.crew.ID, f.users[0].ID, time.Now(), time.Now().Add(14*24*time.Hour))
	if err != nil || len(proposals) != 1 || proposals[0].Start.UTC().Format(time.RFC3339) != date+"T18:00:00Z" {
		t.Fatal("exact suggestion instant not persisted")
	}
}
