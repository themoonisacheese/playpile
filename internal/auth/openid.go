// Package auth implements "Sign in through Steam" with OpenID 2.0.
package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultEndpoint is Steam's OpenID provider.
const DefaultEndpoint = "https://steamcommunity.com/openid/login"

// ClaimedIDPrefix is the prefix of the claimed identifier Steam returns.
const ClaimedIDPrefix = "https://steamcommunity.com/openid/id/"

// ErrVerification means Steam refused or the response was malformed.
var ErrVerification = errors.New("auth: steam login could not be verified")

// OpenID talks to Steam's OpenID endpoint.
type OpenID struct {
	Endpoint string
	HTTP     *http.Client
}

// NewOpenID returns an OpenID helper with sane defaults.
func NewOpenID() *OpenID {
	return &OpenID{
		Endpoint: DefaultEndpoint,
		HTTP:     &http.Client{Timeout: 15 * time.Second},
	}
}

// RedirectURL builds the "checkid_setup" URL the browser should be sent to.
// realm is the site origin, returnTo the full callback URL. The nonce is echoed
// back inside returnTo so the callback can prove it belongs to a login this
// browser started.
func (o *OpenID) RedirectURL(realm, returnTo, nonce string) string {
	u, _ := url.Parse(o.endpoint())
	ret := withQuery(returnTo, "n", nonce)
	q := u.Query()
	q.Set("openid.ns", "http://specs.openid.net/auth/2.0")
	q.Set("openid.mode", "checkid_setup")
	q.Set("openid.return_to", ret)
	q.Set("openid.realm", realm)
	q.Set("openid.identity", "http://specs.openid.net/auth/2.0/identifier_select")
	q.Set("openid.claimed_id", "http://specs.openid.net/auth/2.0/identifier_select")
	u.RawQuery = q.Encode()
	return u.String()
}

// Verify checks the callback parameters with Steam and returns the SteamID64.
func (o *OpenID) Verify(ctx context.Context, params url.Values) (string, error) {
	claimed := params.Get("openid.claimed_id")
	if !strings.HasPrefix(claimed, ClaimedIDPrefix) {
		return "", fmt.Errorf("%w: unexpected claimed_id", ErrVerification)
	}
	steamID := strings.TrimPrefix(claimed, ClaimedIDPrefix)
	if !isDigits(steamID) || len(steamID) < 15 {
		return "", fmt.Errorf("%w: malformed steamid", ErrVerification)
	}

	form := url.Values{}
	for k, v := range params {
		if !strings.HasPrefix(k, "openid.") || len(v) == 0 {
			continue
		}
		form.Set(k, v[0])
	}
	form.Set("openid.mode", "check_authentication")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrVerification, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/plain")

	resp, err := o.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrVerification, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrVerification, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: status %d", ErrVerification, resp.StatusCode)
	}
	if !isValid(string(body)) {
		return "", fmt.Errorf("%w: steam said invalid", ErrVerification)
	}
	return steamID, nil
}

func (o *OpenID) endpoint() string {
	if strings.TrimSpace(o.Endpoint) == "" {
		return DefaultEndpoint
	}
	return o.Endpoint
}

// isValid parses Steam's key:value response for is_valid:true.
func isValid(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "is_valid") {
			return strings.EqualFold(strings.TrimSpace(value), "true")
		}
	}
	return false
}

func withQuery(rawURL, key, value string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return u.String()
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
