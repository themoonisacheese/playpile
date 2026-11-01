package web

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSchedulingShowsAndManagesProposalBeyondAvailabilityView(t *testing.T) {
	f := newPlanningFixture(t)
	date := time.Now().UTC().AddDate(0, 0, 20).Format("2006-01-02")
	f.post(t, 0, "/proposals", url.Values{"title": {"Later game night"}, "proposal_start": {date + "T18:00"}, "proposal_end": {date + "T20:00"}}, 303)
	resp, body := do(t, f.clients[0], "GET", f.target, nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, "Later game night") || !strings.Contains(body, "next 28 days") {
		t.Fatal("accepted day20 proposal must remain visible")
	}
	queryStart := time.Now()
	proposals, err := f.st.Proposals(f.crew.ID, f.users[0].ID, queryStart, queryStart.Add(28*24*time.Hour))
	if err != nil || len(proposals) != 1 {
		t.Fatalf("proposals %v %v", proposals, err)
	}
	path := fmt.Sprintf("/proposals/%d", proposals[0].ID)
	if !strings.Contains(body, path+"/rsvp") || !strings.Contains(body, path+"/cancel") {
		t.Fatal("visible proposal missing RSVP/cancel forms")
	}
	f.post(t, 1, path+"/rsvp", url.Values{"response": {"yes"}}, 303)
	f.post(t, 0, path+"/cancel", nil, 303)
	_, body = do(t, f.clients[0], "GET", f.target, nil, nil)
	if strings.Contains(body, "<h3>Later game night</h3>") {
		t.Fatal("cancelled day20 proposal still visible")
	}
}
