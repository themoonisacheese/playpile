package web

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"playpile/internal/store"
)

type planningFixture struct {
	ts      *httptest.Server
	st      *store.Store
	crew    store.Crew
	users   []store.User
	clients []*http.Client
	tokens  []string
	target  string
}

func newPlanningFixture(t *testing.T) planningFixture {
	t.Helper()
	ts, st := newApp(t)
	f := planningFixture{ts: ts, st: st}
	for _, name := range []string{"Ada", "Bo", "Cy", "Outsider"} {
		c := newClient(t)
		resp, body := do(t, c, "POST", ts.URL+"/dev-login", url.Values{"name": {name}, "steamid": {name}}, nil)
		if resp.StatusCode != 303 {
			t.Fatalf("login: %d %s", resp.StatusCode, body)
		}
		u, err := st.UserBySteamID(name)
		if err != nil {
			t.Fatal(err)
		}
		f.clients = append(f.clients, c)
		f.users = append(f.users, u)
	}
	crew, err := st.CreateCrew("Friday", "US", "english", f.users[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	f.crew = crew
	f.target = fmt.Sprintf("%s/c/%d/schedule", ts.URL, crew.ID)
	invite, err := st.CreateInvite(crew.ID, f.users[0].ID, store.RoleMember, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 3; i++ {
		if _, err := st.RedeemInvite(invite.Token, f.users[i].ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range f.clients {
		f.tokens = append(f.tokens, csrfFrom(t, c, ts.URL+"/"))
	}
	return f
}
func (f planningFixture) post(t *testing.T, who int, path string, form url.Values, want int) string {
	t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf", f.tokens[who])
	if !form.Has("tz") {
		form.Set("tz", "UTC")
	}
	resp, body := do(t, f.clients[who], "POST", f.target+path, form, nil)
	if resp.StatusCode != want {
		t.Fatalf("POST %s: got%d want%d: %s", path, resp.StatusCode, want, body)
	}
	if want == 303 && !strings.Contains(resp.Header.Get("Location"), "tz=") {
		t.Fatal("redirect lost timezone")
	}
	return body
}
func TestSchedulingHTTPFlow(t *testing.T) {
	f := newPlanningFixture(t)
	date := time.Now().UTC().AddDate(0, 0, 2).Format("2006-01-02")
	_, body := do(t, f.clients[0], "GET", f.target, nil, nil)
	if !strings.Contains(body, "Unknown — not answered") || !strings.Contains(body, `method="post"`) {
		t.Fatal("missing usable no-JS day forms")
	}
	available := func() url.Values {
		return url.Values{"date": {date}, "status": {"available"}, "start": {"18:00"}, "end": {"22:00"}}
	}
	// A forged actor ID cannot change another member's availability.
	form := available()
	form.Set("user_id", fmt.Sprint(f.users[1].ID))
	f.post(t, 0, "/availability", form, 303)
	days, _ := f.st.AvailabilityDays(f.crew.ID, f.users[0].ID, time.Now(), time.Now().Add(14*24*time.Hour))
	if len(days) != 1 || days[0].UserID != f.users[0].ID {
		t.Fatal("forged actor changed another user")
	}
	f.post(t, 1, "/availability", available(), 303)
	_, body = do(t, f.clients[0], "GET", f.target, nil, nil)
	if !strings.Contains(body, "Needs an answer for part/all of this window: Cy") {
		t.Fatalf("unknown members must be pending in near match: %s", body)
	}
	f.post(t, 2, "/availability", url.Values{"date": {date}, "status": {"unavailable"}}, 303)
	_, body = do(t, f.clients[0], "GET", f.target, nil, nil)
	if !strings.Contains(body, "Cannot make the whole window: Cy") || !strings.Contains(body, "Needs an answer for part/all of this window: none") {
		t.Fatal("explicit no must not be pending")
	}
	f.post(t, 1, "/availability", available(), 303)
	f.post(t, 2, "/availability", available(), 303)
	_, body = do(t, f.clients[0], "GET", f.target, nil, nil)
	if strings.Contains(body, "No whole-crew window yet") || !strings.Contains(body, "Use this window") {
		t.Fatal("whole crew shared window missing")
	}
	resp, _ := do(t, f.clients[0], "GET", f.target+"?tz=UTC&date="+date, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatal("saved date GET")
	}
	_, body = do(t, f.clients[0], "GET", f.target+"?tz=UTC&date="+date, nil, nil)
	if !strings.Contains(body, `value="18:00"`) {
		t.Fatal("saved editor not prepopulated")
	}
	suggestion := regexp.MustCompile(`<a class="btn btn-sm btn-ghost" href="([^"]+)">Use this window`).FindStringSubmatch(body)
	if len(suggestion) != 2 {
		t.Fatal("no suggestion action")
	}
	selectedURL, err := url.Parse(html.UnescapeString(suggestion[1]))
	if err != nil {
		t.Fatal(err)
	}
	if selectedURL.Query().Get("proposal_start_instant") == "" || selectedURL.Query().Get("proposal_end_instant") == "" {
		t.Fatal("generated suggestion link lost exact instants")
	}
	resp, prefilled := do(t, f.clients[0], "GET", f.ts.URL+html.UnescapeString(suggestion[1]), nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(prefilled, `value="`+date+`T18:00"`) {
		t.Fatal("suggestion did not prefill no-JS proposal")
	}
	proposalForm := selectedURL.Query()
	proposalForm.Set("title", "Minecraft night")
	f.post(t, 0, "/proposals", proposalForm, 303)
	proposals, err := f.st.Proposals(f.crew.ID, f.users[0].ID, time.Now(), time.Now().Add(14*24*time.Hour))
	if err != nil || len(proposals) != 1 {
		t.Fatalf("proposals: %v %v", proposals, err)
	}
	id := proposals[0].ID
	path := fmt.Sprintf("/proposals/%d", id)
	for i, response := range []string{"yes", "maybe", "no"} {
		f.post(t, i, path+"/rsvp", url.Values{"response": {response}, "user_id": {fmt.Sprint(f.users[(i+1)%3].ID)}}, 303)
	}
	proposals, _ = f.st.Proposals(f.crew.ID, f.users[0].ID, time.Now(), time.Now().Add(14*24*time.Hour))
	for i, response := range []string{"yes", "maybe", "no"} {
		if proposals[0].Responses[f.users[i].ID] != response {
			t.Fatal("RSVP applied to wrong user")
		}
	}
	f.post(t, 1, path+"/rsvp", url.Values{"response": {""}}, 303)
	_, body = do(t, f.clients[0], "GET", f.target, nil, nil)
	if !strings.Contains(body, "Bo</strong>: pending") {
		t.Fatal("cleared RSVP must show pending")
	}
	f.post(t, 1, path+"/cancel", nil, 404)
	if err := f.st.SetMemberRole(f.crew.ID, f.users[1].ID, store.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	f.post(t, 1, path+"/cancel", nil, 303)
	_, body = do(t, f.clients[0], "GET", f.target, nil, nil)
	if strings.Contains(body, "<h3>Minecraft night</h3>") {
		t.Fatal("cancelled session still upcoming")
	}
	f.post(t, 0, "/proposals", url.Values{"proposal_start": {date + "T18:00"}, "proposal_end": {date + "T19:00"}}, 303)
	remaining, _ := f.st.Proposals(f.crew.ID, f.users[0].ID, time.Now(), time.Now().Add(14*24*time.Hour))
	for _, p := range remaining {
		if !p.Cancelled {
			f.post(t, 0, fmt.Sprintf("/proposals/%d/cancel", p.ID), nil, 303)
		}
	}
	f.post(t, 0, "/availability", url.Values{"date": {date}, "status": {"unknown"}}, 303)
	_, body = do(t, f.clients[0], "GET", f.target+"?date="+date, nil, nil)
	if !strings.Contains(body, `value="" selected`) {
		t.Fatal("clear did not restore unknown")
	}
}
func TestSchedulingSecurityAndValidation(t *testing.T) {
	f := newPlanningFixture(t)
	date := time.Now().UTC().AddDate(0, 0, 2).Format("2006-01-02")
	for _, path := range []string{"/availability", "/proposals", "/proposals/1/rsvp", "/proposals/1/cancel"} {
		resp, _ := do(t, f.clients[0], "POST", f.target+path, url.Values{"tz": {"UTC"}}, nil)
		if resp.StatusCode != 403 {
			t.Fatalf("missing CSRF %s=%d", path, resp.StatusCode)
		}
		resp, _ = do(t, f.clients[0], "POST", f.target+path, nil, map[string]string{"Content-Type": "application/json"})
		if resp.StatusCode != 403 {
			t.Fatalf("JSON bypass %s=%d", path, resp.StatusCode)
		}
		f.post(t, 3, path, nil, 403)
	}
	resp, _ := do(t, f.clients[3], "GET", f.target, nil, nil)
	if resp.StatusCode != 403 {
		t.Fatal("outsider read")
	}
	body := f.post(t, 0, "/availability", url.Values{"date": {date}, "status": {"available"}, "start": {"22:00"}, "end": {"02:00"}}, 400)
	if !strings.Contains(body, "tick ends next day") || !strings.Contains(body, `value="22:00"`) {
		t.Fatal("missing actionable overnight error/preserved fields")
	}
	f.post(t, 0, "/availability", url.Values{"date": {date}, "status": {"available"}, "start": {"22:00"}, "end": {"02:00"}, "overnight0": {"1"}}, 303)
	_, body = do(t, f.clients[0], "GET", f.target, nil, nil)
	if !strings.Contains(body, "(&#43;1 day)") {
		t.Fatalf("missing overnight label: %s", body)
	}
	resp, body = do(t, f.clients[0], "GET", f.target+"?tz=America%2FNew_York", nil, nil)
	if resp.StatusCode != 400 || !strings.Contains(body, "clear each future dated answer") {
		t.Fatalf("timezone conflict status=%d body=%s", resp.StatusCode, body)
	}
	f.post(t, 0, "/proposals/999999/rsvp", url.Values{"response": {"yes"}}, 404)
	f.post(t, 0, "/availability", url.Values{"date": {date}, "status": {"available"}}, 400)
	f.post(t, 0, "/availability", url.Values{"date": {date}, "status": {""}}, 400)
	body = f.post(t, 0, "/proposals", url.Values{"title": {"My preserved title"}, "proposal_start": {date + "T20:00"}, "proposal_end": {date + "T19:00"}}, 400)
	if !strings.Contains(body, `value="My preserved title"`) || !strings.Contains(body, "12 hours") {
		t.Fatal("proposal input/error not preserved")
	}
	// Body cap is applied before form parsing; an oversized form never writes.
	f.post(t, 0, "/availability", url.Values{"padding": {strings.Repeat("x", 33<<10)}}, 413)
	other, err := f.st.CreateCrew("Elsewhere", "US", "english", f.users[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.st.CreateProposal(other.ID, f.users[0].ID, "other", time.Now().Add(time.Hour).Truncate(time.Minute), time.Now().Add(2*time.Hour).Truncate(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	f.post(t, 0, fmt.Sprintf("/proposals/%d/cancel", p.ID), nil, 404)
	f.post(t, 0, fmt.Sprintf("/proposals/%d/rsvp", p.ID), url.Values{"response": {"yes"}}, 404)
}
func TestSchedulingTimezonesAndDST(t *testing.T) {
	f := newPlanningFixture(t)
	date := time.Now().In(time.FixedZone("JST", 9*3600)).AddDate(0, 0, 2).Format("2006-01-02")
	f.post(t, 0, "/availability", url.Values{"tz": {"Asia/Tokyo"}, "date": {date}, "status": {"available"}, "start": {"18:00"}, "end": {"21:00"}}, 303)
	days, err := f.st.AvailabilityDays(f.crew.ID, f.users[0].ID, time.Now(), time.Now().Add(14*24*time.Hour))
	if err != nil || len(days) != 1 {
		t.Fatalf("days %v %v", days, err)
	}
	if days[0].Windows[0].Start.UTC().Hour() != 9 {
		t.Fatal("local timezone was not converted to UTC")
	}
	f.post(t, 1, "/availability", url.Values{"date": {date}, "status": {"unavailable"}}, 303)
	f.post(t, 0, "/proposals", url.Values{"tz": {"Asia/Tokyo"}, "proposal_start": {date + "T18:00"}, "proposal_end": {date + "T19:00"}}, 303)
	_, body := do(t, f.clients[0], "GET", f.target, nil, nil)
	link := regexp.MustCompile(`<a href="([^"]+)">Session link</a>`).FindStringSubmatch(body)
	if len(link) != 2 || strings.Contains(link[1], "?") {
		t.Fatal("session share link must use recipient's clock")
	}
	sharedResp, sharedBody := do(t, f.clients[1], "GET", f.ts.URL+html.UnescapeString(link[1]), nil, nil)
	if sharedResp.StatusCode != 200 || !strings.Contains(sharedBody, "Game night") || !strings.Contains(sharedBody, `value="UTC"`) {
		t.Fatal("cross-zone shared session did not open in recipient timezone")
	}
	if !strings.Contains(body, `value="Asia/Tokyo"`) {
		t.Fatal("saved zone not default")
	}
	for _, zone := range []string{"Local", "not/a-zone"} {
		resp, _ := do(t, f.clients[0], "GET", f.target+"?tz="+url.QueryEscape(zone), nil, nil)
		if resp.StatusCode != 400 {
			t.Fatalf("zone %s accepted", zone)
		}
	}
	for _, input := range []string{"2030-03-10T02:30", "2030-11-03T01:30"} {
		f.post(t, 1, "/proposals", url.Values{"tz": {"America/New_York"}, "proposal_start": {input}, "proposal_end": {input[:10] + "T04:00"}}, 400)
	}
	if _, _, err := scheduleProposalTimes("2030-01-02X18:00", "2030-01-02T19:00", "UTC"); err == nil {
		t.Fatal("malformed datetime accepted")
	}
	start, end, err := scheduleProposalTimes("2030-01-02T23:59", "2030-01-03T01:00", "UTC")
	if err != nil || end.Sub(start) != 61*time.Minute {
		t.Fatalf("23:59 endpoint: %v %v %v", start, end, err)
	}
}
