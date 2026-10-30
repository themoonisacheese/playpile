package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// loginAsDev signs a client in as a fixed dev account, so the test can find the
// same user in the store afterwards and give it an avatar.
func loginAsDev(t *testing.T, ts *httptest.Server, c *http.Client, name, steamID string) {
	t.Helper()
	resp, body := do(t, c, "POST", ts.URL+"/dev-login", url.Values{"name": {name}, "steamid": {steamID}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("dev-login %s = %d: %s", steamID, resp.StatusCode, body)
	}
}

// TestOwnerAvatars checks the rendered ownership bubbles: members with a Steam
// avatar show the picture, members without one keep the initial, and a display
// name is always escaped into the title attribute.
func TestOwnerAvatars(t *testing.T) {
	ts, st := newApp(t)

	ada := newClient(t)
	loginAsDev(t, ts, ada, "Ada", "dev-ada")
	csrf := csrfFrom(t, ada, ts.URL+"/")
	resp, body := do(t, ada, "POST", ts.URL+"/crews",
		url.Values{"name": {"Friday"}, "country": {"US"}, "csrf": {csrf}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create crew = %d: %s", resp.StatusCode, body)
	}
	loc := resp.Header.Get("Location")
	crewURL := ts.URL + strings.Split(loc, "?")[0]
	crewID, err := strconv.ParseInt(strings.TrimPrefix(crewURL, ts.URL+"/c/"), 10, 64)
	if err != nil {
		t.Fatalf("crew url %q: %v", crewURL, err)
	}

	// One game on the list, added the way the search box adds it.
	req, _ := http.NewRequest("POST", crewURL+"/entries", strings.NewReader(`{"appid":440}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Playpile", "fetch")
	addResp, err := ada.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	addResp.Body.Close()
	if addResp.StatusCode != http.StatusOK {
		t.Fatalf("add game = %d", addResp.StatusCode)
	}

	adaUser, err := st.UserBySteamID("dev-ada")
	if err != nil {
		t.Fatal(err)
	}
	// Bo joins without a Steam avatar at all.
	boUser, err := st.UpsertSteamUser("dev-bo", "Bo", "", "")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := st.CreateInvite(crewID, adaUser.ID, "member", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RedeemInvite(inv.Token, boUser.ID); err != nil {
		t.Fatal(err)
	}

	// Ada gets an avatar, and a name that must come out escaped.
	const adaAvatar = "https://avatars.example/ada_184.png"
	if _, err := st.UpsertSteamUser("dev-ada", `Ada "Ace" <b>`, adaAvatar, ""); err != nil {
		t.Fatal(err)
	}

	// One game went in, so it owns the only entry id in a fresh database.
	first, err := st.EntryByID(crewID, 1)
	if err != nil || first.AppID != 440 {
		t.Fatalf("first entry = %+v (%v)", first, err)
	}
	entryID := strconv.FormatInt(first.ID, 10)
	own := func(c *http.Client, csrf string) string {
		t.Helper()
		resp, body := do(t, c, "POST", crewURL+"/entries/"+entryID+"/own",
			url.Values{"owned": {"1"}, "csrf": {csrf}}, map[string]string{"X-Playpile": "fetch"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("own entry = %d: %s", resp.StatusCode, body)
		}
		return body
	}
	own(ada, csrfFrom(t, ada, crewURL))

	bo := newClient(t)
	loginAsDev(t, ts, bo, "Bo", "dev-bo")
	body = own(bo, csrfFrom(t, bo, crewURL))

	// The AJAX reply carries the owners fragment the page swaps in.
	var payload struct {
		OwnersHTML string `json:"owners_html"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("own reply is not JSON: %v: %s", err, body)
	}
	frag := payload.OwnersHTML
	if frag == "" {
		t.Fatalf("own reply has no owners fragment: %s", body)
	}

	// Ada has an avatar: exactly one picture, sitting on her bubble.
	if strings.Count(frag, "owner-avatar") != 1 {
		t.Errorf("want one avatar image, got %d in %s", strings.Count(frag, "owner-avatar"), frag)
	}
	if !strings.Contains(frag, `src="`+adaAvatar+`"`) {
		t.Errorf("avatar src missing from %s", frag)
	}
	// Both bubbles keep the initial underneath, which is what shows if the
	// image cannot load. Bo has no avatar at all, so his bubble is letter-only.
	if !strings.Contains(frag, `<span class="owner-initial">A</span>`) {
		t.Errorf("Ada's initial fallback missing from %s", frag)
	}
	if !strings.Contains(frag, `<span class="owner-initial">B</span>`) {
		t.Errorf("Bo's initial fallback missing from %s", frag)
	}
	if strings.Contains(frag, "dev-bo") {
		t.Errorf("Bo has no avatar but got an image: %s", frag)
	}
	// Names stay escaped, and the fallback needs no inline handler.
	if !strings.Contains(frag, `title="Ada &#34;Ace&#34; &lt;b&gt;"`) {
		t.Errorf("escaped name missing from %s", frag)
	}
	if !strings.Contains(frag, `title="Bo"`) {
		t.Errorf("name title missing from %s", frag)
	}
	for _, banned := range []string{"onerror", "onload"} {
		if strings.Contains(frag, banned) {
			t.Errorf("inline handler %q in owners fragment: %s", banned, frag)
		}
	}
	if !strings.Contains(frag, "owner--me") {
		t.Errorf("the viewer's own bubble lost its styling: %s", frag)
	}

	// The page renders the very same partial, so its bubbles carry the avatar
	// too; check the row the list actually shows.
	_, page := do(t, ada, "GET", crewURL, nil, nil)
	if !strings.Contains(page, `<div class="owners-row">`) {
		t.Fatalf("crew page rendered no owners row:\n%s", page)
	}
	if !strings.Contains(page, `src="`+adaAvatar+`"`) {
		t.Error("crew page is missing the owner avatar")
	}
	if !strings.Contains(page, `<span class="owner-initial">B</span>`) {
		t.Error("crew page is missing the letter-only owner")
	}
}
