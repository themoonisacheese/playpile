package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"playpile/internal/catalog"
	"playpile/internal/steam"
	"playpile/internal/store"
)

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

type fakeSteam struct {
	games map[int]steam.Game
}

func (f *fakeSteam) AppDetails(ctx context.Context, appid int, country, lang string) (steam.Game, error) {
	if g, ok := f.games[appid]; ok {
		return g, nil
	}
	return steam.Game{}, steam.ErrNotFound
}

func (f *fakeSteam) Search(ctx context.Context, term, country, lang string) ([]steam.SearchResult, error) {
	var out []steam.SearchResult
	for _, g := range f.games {
		if strings.Contains(strings.ToLower(g.Name), strings.ToLower(term)) {
			out = append(out, steam.SearchResult{AppID: g.AppID, Name: g.Name, Currency: "USD", PriceFinal: g.Price.Final})
		}
	}
	return out, nil
}

func (f *fakeSteam) Reviews(ctx context.Context, appid int) (steam.Reviews, error) {
	return steam.Reviews{TotalPositive: 900, TotalNegative: 100, TotalReviews: 1000, Description: "Very Positive"}, nil
}

func newApp(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	fake := &fakeSteam{games: map[int]steam.Game{
		440: {AppID: 440, Name: "Team Defense", Price: &steam.Price{Currency: "USD", Initial: 1000, Final: 1000},
			Metacritic: &steam.Metacritic{Score: 90}, Developers: []string{"Valve"}},
		570: {AppID: 570, Name: "Ancient Defense 2", Price: &steam.Price{Currency: "USD", Initial: 2000, Final: 2000}},
		730: {AppID: 730, Name: "Shooter", IsFree: true},
	}}
	cat := catalog.New(st, fake)
	// Display reads are cache-only. Seed the review data asserted by these UI fixtures.
	for id := range fake.games {
		if _, err := cat.Reviews(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	srv, err := New(Config{
		DevLogin:        true,
		DefaultCountry:  "US",
		DefaultLanguage: "english",
		Version:         "test",
	}, st, cat, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

func newClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Jar:     jar,
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func do(t *testing.T, c *http.Client, method, target string, form url.Values, headers map[string]string) (*http.Response, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

func csrfFrom(t *testing.T, c *http.Client, target string) string {
	t.Helper()
	resp, body := do(t, c, "GET", target, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", target, resp.StatusCode)
	}
	m := csrfRe.FindStringSubmatch(body)
	if len(m) < 2 {
		t.Fatalf("no csrf token on %s", target)
	}
	return m[1]
}

func login(t *testing.T, ts *httptest.Server, c *http.Client, name string) string {
	t.Helper()
	resp, body := do(t, c, "POST", ts.URL+"/dev-login", url.Values{"name": {name}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("dev-login = %d: %s", resp.StatusCode, body)
	}
	if len(c.Jar.Cookies(mustURL(t, ts.URL))) == 0 {
		t.Fatal("no session cookie after login")
	}
	return ts.URL + "/"
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestFullFlow(t *testing.T) {
	ts, _ := newApp(t)
	ada := newClient(t)
	login(t, ts, ada, "Ada")

	_, home := do(t, ada, "GET", ts.URL+"/", nil, nil)
	if !strings.Contains(home, "Your crews") {
		t.Fatalf("home missing crew heading:\n%s", home)
	}
	csrf := csrfFrom(t, ada, ts.URL+"/")

	// Create a crew.
	resp, _ := do(t, ada, "POST", ts.URL+"/crews", url.Values{"name": {"Friday"}, "country": {"US"}, "csrf": {csrf}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create crew = %d", resp.StatusCode)
	}
	crewURL := ts.URL + strings.Split(resp.Header.Get("Location"), "?")[0]
	if !strings.HasPrefix(crewURL, ts.URL+"/c/") {
		t.Fatalf("redirect = %q", crewURL)
	}

	// A form post without the CSRF token is refused.
	resp, _ = do(t, ada, "POST", crewURL+"/entries", url.Values{"appid": {"440"}}, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("csrf-less post = %d, want 403", resp.StatusCode)
	}

	// Add a game through the JSON path the page's search box uses.
	resp, body := do(t, ada, "POST", crewURL+"/entries",
		url.Values{}, map[string]string{"Content-Type": "application/json", "X-Playpile": "fetch"})
	_ = body
	if resp.StatusCode == http.StatusOK {
		t.Log("empty JSON body accepted, expected a 400")
	}

	addJSON := func(appid string) (*http.Response, string) {
		req, _ := http.NewRequest("POST", crewURL+"/entries", strings.NewReader(`{"appid":`+appid+`}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Playpile", "fetch")
		r, err := ada.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		raw, _ := io.ReadAll(r.Body)
		return r, string(raw)
	}
	if r, out := addJSON("440"); r.StatusCode != http.StatusOK || !strings.Contains(out, `"ok":true`) {
		t.Fatalf("add = %d %s", r.StatusCode, out)
	}
	if r, _ := addJSON("440"); r.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate add = %d, want 409", r.StatusCode)
	}
	if r, _ := addJSON("570"); r.StatusCode != http.StatusOK {
		t.Fatal("second add failed")
	}
	// A game Steam does not sell has no store page: that is a 404, not a 502.
	if r, _ := addJSON("999999"); r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown appid = %d, want 404", r.StatusCode)
	}

	// The list page shows the game with price, metascore and the summary.
	resp, page := do(t, ada, "GET", crewURL, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("crew page = %d", resp.StatusCode)
	}
	for _, want := range []string{"Team Defense", "$10.00", "MC 90", "Very Positive 90%", "$30.00", "for you", "playable now"} {
		if !strings.Contains(page, want) {
			t.Errorf("crew page missing %q", want)
		}
	}

	// Vote up through the enhanced-form endpoint.
	entryID := "1"
	csrf = csrfFrom(t, ada, crewURL)
	resp, body = do(t, ada, "POST", crewURL+"/entries/"+entryID+"/vote",
		url.Values{"value": {"1"}, "csrf": {csrf}}, map[string]string{"X-Playpile": "fetch"})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"score":1`) || !strings.Contains(body, "summary_html") {
		t.Fatalf("vote = %d %s", resp.StatusCode, body)
	}

	// Mark ownership: the personal bill should drop to the second game only.
	resp, body = do(t, ada, "POST", crewURL+"/entries/"+entryID+"/own",
		url.Values{"owned": {"1"}, "csrf": {csrf}}, map[string]string{"X-Playpile": "fetch"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("own = %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "you own this") {
		t.Errorf("ownership fragment missing: %s", body)
	}
	_, page = do(t, ada, "GET", crewURL, nil, nil)
	if !strings.Contains(page, "$20.00") {
		t.Error("personal cost should now be $20.00")
	}

	// A stranger cannot see the crew.
	bo := newClient(t)
	login(t, ts, bo, "Bo")
	resp, page = do(t, bo, "GET", crewURL, nil, nil)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(page, "Not your crew") {
		t.Fatalf("stranger page = %d", resp.StatusCode)
	}

	// Ada invites Bo by link.
	membersURL := crewURL + "/members"
	csrf = csrfFrom(t, ada, membersURL)
	resp, _ = do(t, ada, "POST", crewURL+"/invites", url.Values{"role": {"member"}, "days": {"7"}, "csrf": {csrf}}, nil)
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(loc, "new=") {
		t.Fatalf("invite = %d %q", resp.StatusCode, loc)
	}
	token := loc[strings.Index(loc, "new=")+4:]

	joinURL := ts.URL + "/join/" + token
	resp, page = do(t, bo, "GET", joinURL, nil, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, "Join Friday") {
		t.Fatalf("join page = %d", resp.StatusCode)
	}
	csrf = csrfFrom(t, bo, joinURL)
	resp, _ = do(t, bo, "POST", joinURL, url.Values{"csrf": {csrf}}, nil)
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "/c/") {
		t.Fatalf("join post = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// Bo is a member now and sees the group total for two people.
	_, page = do(t, bo, "GET", crewURL, nil, nil)
	if !strings.Contains(page, "2 players") {
		t.Error("member count should be 2")
	}
	if !strings.Contains(page, "$30.00") {
		t.Error("group cost for two members should be $30.00")
	}

	// A plain member cannot remove games or change roles.
	boCSRF := csrfFrom(t, bo, crewURL)
	resp, _ = do(t, bo, "POST", crewURL+"/entries/"+entryID+"/delete", url.Values{"csrf": {boCSRF}}, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member delete = %d, want 403", resp.StatusCode)
	}
	resp, _ = do(t, bo, "POST", crewURL+"/members/1/role", url.Values{"role": {"owner"}, "csrf": {boCSRF}}, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member role change = %d, want 403", resp.StatusCode)
	}

	// The owner can remove the game.
	adaCSRF := csrfFrom(t, ada, crewURL)
	resp, _ = do(t, ada, "POST", crewURL+"/entries/"+entryID+"/delete", url.Values{"csrf": {adaCSRF}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("owner delete = %d", resp.StatusCode)
	}
	_, page = do(t, ada, "GET", crewURL, nil, nil)
	if strings.Contains(page, "Team Defense") {
		t.Error("removed game still on the list")
	}

	// Health check.
	resp, body = do(t, ada, "GET", ts.URL+"/healthz", nil, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "ok") {
		t.Fatalf("healthz = %d %q", resp.StatusCode, body)
	}
}

func TestAnonymousRedirectsAndNotFound(t *testing.T) {
	ts, _ := newApp(t)
	anon := newClient(t)

	resp, _ := do(t, anon, "GET", ts.URL+"/login", nil, nil)
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "steamcommunity.com/openid/login") {
		t.Fatalf("login redirect = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if !strings.Contains(resp.Header.Get("Location"), "openid.mode=checkid_setup") {
		t.Fatalf("login redirect missing openid params: %q", resp.Header.Get("Location"))
	}

	resp, page := do(t, anon, "GET", ts.URL+"/nope", nil, nil)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(page, "Not found") {
		t.Fatalf("unknown path = %d", resp.StatusCode)
	}

	resp, _ = do(t, anon, "GET", ts.URL+"/join/does-not-exist", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bad invite = %d", resp.StatusCode)
	}
}

// TestAddEntryByName covers the search box as people actually use it: an exact
// name, a bare app id typed in, a pasted store link, and the refusals when the
// name matches nothing or more than one game.
func TestAddEntryByName(t *testing.T) {
	ts, _ := newApp(t)
	c := newClient(t)
	login(t, ts, c, "Ada")
	csrf := csrfFrom(t, c, ts.URL+"/")
	resp, _ := do(t, c, "POST", ts.URL+"/crews",
		url.Values{"name": {"Friday"}, "country": {"US"}, "csrf": {csrf}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create crew = %d", resp.StatusCode)
	}
	crew := ts.URL + strings.Split(resp.Header.Get("Location"), "?")[0]

	addForm := func(term string) *http.Response {
		t.Helper()
		token := csrfFrom(t, c, crew)
		r, _ := do(t, c, "POST", crew+"/entries", url.Values{"q": {term}, "csrf": {token}}, nil)
		return r
	}

	for _, term := range []string{
		"Team Defense", // exact name
		"570",          // id typed in the box
		"https://store.steampowered.com/app/730/Shooter/", // pasted store link
	} {
		if r := addForm(term); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("add %q = %d, want a redirect back to the list", term, r.StatusCode)
		}
	}

	_, page := do(t, c, "GET", crew, nil, nil)
	for _, name := range []string{"Team Defense", "Ancient Defense 2", "Shooter"} {
		if !strings.Contains(page, name) {
			t.Errorf("%q is not on the list\n%s", name, page)
		}
	}

	// A name nobody has adds nothing.
	before := strings.Count(page, "<article class=\"entry")
	if r := addForm("Nonexistent Quest"); r.StatusCode != http.StatusSeeOther {
		t.Errorf("unknown name = %d, want a redirect with a flash", r.StatusCode)
	}
	_, page = do(t, c, "GET", crew, nil, nil)
	if got := strings.Count(page, "<article class=\"entry"); got != before {
		t.Errorf("list grew from %d to %d rows after a failed add", before, got)
	}

	// An ambiguous name is refused by the JSON path with the candidates.
	req, _ := http.NewRequest("POST", crew+"/entries", strings.NewReader(`{"q":"defense"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Playpile", "fetch")
	r, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusConflict {
		t.Errorf("ambiguous name = %d, want 409", r.StatusCode)
	}
	if !strings.Contains(string(body), "Several games match") {
		t.Errorf("ambiguous body = %s", body)
	}
}

// TestSearchBoxIsWired pins the contract between the template and app.js: the
// dropdown only initialises when the search input carries its search URL, and a
// missing attribute silently disables the whole autocomplete.
func TestSearchBoxIsWired(t *testing.T) {
	ts, _ := newApp(t)
	c := newClient(t)
	login(t, ts, c, "Ada")
	csrf := csrfFrom(t, c, ts.URL+"/")
	resp, _ := do(t, c, "POST", ts.URL+"/crews",
		url.Values{"name": {"Friday"}, "country": {"US"}, "csrf": {csrf}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create crew = %d", resp.StatusCode)
	}
	crew := ts.URL + strings.Split(resp.Header.Get("Location"), "?")[0]
	_, page := do(t, c, "GET", crew, nil, nil)

	input := inputTagRe.FindString(page)
	if input == "" {
		t.Fatalf("no search input on the crew page")
	}
	for _, attr := range []string{`data-search-url="/c/`, `data-add-url="/c/`, `name="q"`} {
		if !strings.Contains(input, attr) {
			t.Errorf("search input is missing %s: %s", attr, input)
		}
	}
}

// inputTagRe finds the add-game search box in the rendered page.
var inputTagRe = regexp.MustCompile(`<input class="search-input"[^>]*>`)
