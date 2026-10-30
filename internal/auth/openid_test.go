package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRedirectURL(t *testing.T) {
	o := NewOpenID()
	got := o.RedirectURL("https://g.example", "https://g.example/auth/steam/callback", "abc123")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("openid.mode") != "checkid_setup" {
		t.Fatalf("mode = %q", q.Get("openid.mode"))
	}
	if q.Get("openid.realm") != "https://g.example" {
		t.Fatalf("realm = %q", q.Get("openid.realm"))
	}
	if !strings.Contains(q.Get("openid.return_to"), "n=abc123") {
		t.Fatalf("return_to = %q, want nonce echoed", q.Get("openid.return_to"))
	}
	if !strings.HasPrefix(got, "https://steamcommunity.com/openid/login?") {
		t.Fatalf("unexpected endpoint: %s", got)
	}
}

func TestVerify(t *testing.T) {
	var sawMode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sawMode = r.PostForm.Get("openid.mode")
		w.Header().Set("Content-Type", "text/plain")
		if strings.Contains(r.PostForm.Get("openid.sig"), "bad") {
			w.Write([]byte("ns:http://specs.openid.net/auth/2.0\nis_valid:false\n"))
			return
		}
		w.Write([]byte("ns:http://specs.openid.net/auth/2.0\nis_valid:true\n"))
	}))
	defer srv.Close()

	o := NewOpenID()
	o.Endpoint = srv.URL

	params := url.Values{
		"openid.mode":       {"id_res"},
		"openid.claimed_id": {ClaimedIDPrefix + "76561197960287930"},
		"openid.sig":        {"good"},
	}
	id, err := o.Verify(context.Background(), params)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if id != "76561197960287930" {
		t.Fatalf("id = %q", id)
	}
	if sawMode != "check_authentication" {
		t.Fatalf("posted mode = %q", sawMode)
	}

	params.Set("openid.sig", "bad")
	if _, err := o.Verify(context.Background(), params); !errors.Is(err, ErrVerification) {
		t.Fatalf("expected ErrVerification, got %v", err)
	}

	params.Set("openid.sig", "good")
	params.Set("openid.claimed_id", "https://evil.example/openid/id/123456789012345")
	if _, err := o.Verify(context.Background(), params); !errors.Is(err, ErrVerification) {
		t.Fatalf("expected ErrVerification for foreign claimed_id, got %v", err)
	}
}
