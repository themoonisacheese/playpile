package web

import (
	"fmt"
	"net/http"
	"net/url"
	"playpile/internal/store"
	"strings"
	"testing"
)

func TestManualGameFlow(t *testing.T) {
	ts, st := newApp(t)
	owner := newClient(t)
	do(t, owner, "POST", ts.URL+"/dev-login", url.Values{"name": {"Ada"}, "steamid": {"manual-ada"}}, nil)
	token := csrfFrom(t, owner, ts.URL+"/")
	resp, _ := do(t, owner, "POST", ts.URL+"/crews", url.Values{"name": {"Manual crew"}, "country": {"FR"}, "csrf": {token}}, nil)
	if resp.StatusCode != 303 {
		t.Fatal(resp.StatusCode)
	}
	crewURL := ts.URL + strings.Split(resp.Header.Get("Location"), "?")[0]
	resp, body := do(t, owner, "GET", crewURL+"/manual/new", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, "Price per person (EUR)") {
		t.Fatalf("form: %d %s", resp.StatusCode, body)
	}
	fields := url.Values{"name": {"Minecraft"}, "description": {"Build together <3"}, "store_url": {"https://www.minecraft.net/"}, "price": {"29.99"}, "metascore": {"0"}, "csrf": {token}}
	resp, _ = do(t, owner, "POST", crewURL+"/manual", url.Values{"name": {"Missing token"}}, nil)
	if resp.StatusCode != 403 {
		t.Fatal("CSRF was bypassed")
	}
	resp, _ = do(t, owner, "POST", crewURL+"/manual", fields, nil)
	if resp.StatusCode != 303 {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	for _, tc := range []struct{ name, price string }{{"Unknown price", ""}, {"Free game", "0"}} {
		resp, _ = do(t, owner, "POST", crewURL+"/manual", url.Values{"name": {tc.name}, "price": {tc.price}, "csrf": {token}}, nil)
		if resp.StatusCode != 303 {
			t.Fatalf("create %s: %d", tc.name, resp.StatusCode)
		}
	}
	entries, err := st.Entries(1, 1)
	if err != nil || len(entries) != 3 {
		t.Fatalf("entries: %v %v", entries, err)
	}
	minecraft := entries[0]
	if minecraft.AppID != 0 || minecraft.Manual == nil || minecraft.Manual.PriceMinor == nil || *minecraft.Manual.PriceMinor != 2999 {
		t.Fatalf("manual identity/data: %+v", minecraft)
	}
	if entries[1].Manual.PriceMinor != nil || *entries[2].Manual.PriceMinor != 0 {
		t.Fatal("unknown and free were conflated")
	}
	detailURL := fmt.Sprintf("%s/entries/%d", crewURL, minecraft.ID)
	resp, body = do(t, owner, "GET", detailURL, nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, "MC 0") || !strings.Contains(body, "https://www.minecraft.net/") || strings.Contains(body, "steampowered.com/app/0") {
		t.Fatalf("detail: %d %s", resp.StatusCode, body)
	}
	fields.Set("image_url", "javascript:alert(1)")
	resp, body = do(t, owner, "POST", detailURL+"/edit", fields, nil)
	if resp.StatusCode != 400 || !strings.Contains(body, `value="Minecraft"`) {
		t.Fatal("invalid URL was not rejected with preserved fields")
	}
	loaded, _ := st.EntryByID(1, minecraft.ID)
	if loaded.Manual.ImageURL != "" {
		t.Fatal("invalid edit persisted")
	}
	fields.Set("image_url", "")
	fields.Set("price", "24.50")
	fields.Set("name", "Minecraft Java")
	resp, _ = do(t, owner, "POST", detailURL+"/edit", fields, nil)
	if resp.StatusCode != 303 {
		t.Fatal(resp.StatusCode)
	}
	loaded, _ = st.EntryByID(1, minecraft.ID)
	if loaded.Manual.Name != "Minecraft Java" || *loaded.Manual.PriceMinor != 2450 {
		t.Fatal("edit not saved")
	}
	headers := map[string]string{"X-Playpile": "fetch"}
	resp, body = do(t, owner, "POST", detailURL+"/own", url.Values{"owned": {"1"}, "csrf": {token}}, headers)
	if resp.StatusCode != 200 || !strings.Contains(body, "you own this") {
		t.Fatalf("own: %d %s", resp.StatusCode, body)
	}
	resp, body = do(t, owner, "POST", detailURL+"/vote", url.Values{"value": {"-1"}, "csrf": {token}}, headers)
	if resp.StatusCode != 200 || !strings.Contains(body, `"vetoed":true`) {
		t.Fatalf("veto: %d %s", resp.StatusCode, body)
	}
	bo, err := st.UpsertSteamUser("manual-bo", "Bo", "", "")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := st.CreateInvite(1, 1, store.RoleMember, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RedeemInvite(inv.Token, bo.ID); err != nil {
		t.Fatal(err)
	}
	member := newClient(t)
	do(t, member, "POST", ts.URL+"/dev-login", url.Values{"name": {"Bo"}, "steamid": {"manual-bo"}}, nil)
	resp, _ = do(t, member, "GET", detailURL+"/edit", nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatal("another member could edit creator's game")
	}
	memberToken := csrfFrom(t, member, crewURL)
	resp, _ = do(t, member, "POST", crewURL+"/manual", url.Values{"name": {"Bo's game"}, "csrf": {memberToken}}, nil)
	if resp.StatusCode != 303 {
		t.Fatal("member cannot add manual game")
	}
	stranger := newClient(t)
	login(t, ts, stranger, "Stranger")
	resp, _ = do(t, stranger, "GET", detailURL, nil, nil)
	if resp.StatusCode != 403 {
		t.Fatal("stranger can read crew")
	}
	resp, _ = do(t, owner, "POST", crewURL+"/manual", url.Values{"name": {"Bad price"}, "price": {"NaN"}, "csrf": {token}}, nil)
	if resp.StatusCode != 400 {
		t.Fatal("invalid price accepted")
	}
	// Refresh must not try to send manual IDs to Steam.
	resp, _ = do(t, owner, "POST", crewURL+"/refresh", url.Values{"csrf": {token}}, nil)
	if resp.StatusCode != 303 || strings.Contains(resp.Header.Get("Location"), "kind=error") {
		t.Fatal("manual-only refresh tried Steam")
	}
}
