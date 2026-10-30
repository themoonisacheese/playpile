package steam

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// ErrNotFound is returned when an app, profile or dataset does not exist (or
// is not visible) for the requested region/privacy settings.
var ErrNotFound = errors.New("steam: not found")

// ErrNoAPIKey is returned when an endpoint that requires an API key is
// called on a Client without one configured.
var ErrNoAPIKey = errors.New("steam: no API key configured")

const (
	defaultStoreBase   = "https://store.steampowered.com"
	defaultAPIBase     = "https://api.steampowered.com"
	defaultUserAgent   = "playpile/1.0 (+https://github.com/)"
	defaultMinInterval = 120 * time.Millisecond
	maxBodyBytes       = 8 << 20 // 8 MiB
)

// New returns a Client configured with sane defaults. apiKey may be empty;
// it is only required by PlayerSummary and OwnedGames.
func New(apiKey string) *Client {
	return &Client{
		HTTP:        &http.Client{Timeout: 10 * time.Second},
		StoreBase:   defaultStoreBase,
		APIBase:     defaultAPIBase,
		APIKey:      apiKey,
		UserAgent:   defaultUserAgent,
		MinInterval: defaultMinInterval,
	}
}

// throttle blocks, if necessary, so that consecutive requests made through
// this client are spaced at least MinInterval apart. Safe for concurrent use.
func (c *Client) throttle(ctx context.Context) error {
	c.mu.Lock()
	min := c.MinInterval
	if min <= 0 {
		min = defaultMinInterval
	}
	now := time.Now()
	wait := time.Duration(0)
	if !c.lastCall.IsZero() {
		elapsed := now.Sub(c.lastCall)
		if elapsed < min {
			wait = min - elapsed
		}
	}
	c.lastCall = now.Add(wait)
	c.mu.Unlock()

	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// httpClient returns the configured *http.Client, falling back to a default
// one if HTTP was left nil.
func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// userAgent returns the configured User-Agent, falling back to the default.
func (c *Client) userAgent() string {
	if c.UserAgent != "" {
		return c.UserAgent
	}
	return defaultUserAgent
}

var retryBackoff = []time.Duration{300 * time.Millisecond, 900 * time.Millisecond}

// doGet performs an HTTP GET against rawURL, retrying transient failures
// (network errors, 429, and 5xx) up to len(retryBackoff) extra times. It
// returns the response body, capped at maxBodyBytes. The caller owns the
// returned bytes; the response body is always closed.
func (c *Client) doGet(ctx context.Context, rawURL string) ([]byte, error) {
	if err := c.throttle(ctx); err != nil {
		return nil, err
	}

	var lastErr error
	attempts := 1 + len(retryBackoff)
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if attempt > 0 {
			d := retryBackoff[attempt-1]
			t := time.NewTimer(d)
			select {
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			case <-t.C:
			}
		}

		body, retryable, err := c.doGetOnce(ctx, rawURL)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retryable {
			return nil, err
		}
	}
	return nil, lastErr
}

// doGetOnce performs a single GET attempt. retryable reports whether the
// caller should retry on error.
func (c *Client) doGetOnce(ctx context.Context, rawURL string) (body []byte, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent())

	resp, err := c.httpClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		return nil, true, err
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, maxBodyBytes+1)
	data, readErr := io.ReadAll(limited)
	if readErr != nil {
		return nil, true, fmt.Errorf("steam: reading response: %w", readErr)
	}
	if len(data) > maxBodyBytes {
		return nil, false, errors.New("steam: response body too large")
	}

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf("steam: unexpected status %d", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return nil, false, fmt.Errorf("steam: unexpected status %d", resp.StatusCode)
	}
	return data, false, nil
}

// AppDetails fetches and normalises game metadata for appid in the given
// store region (country like "US") and language (like "english").
func (c *Client) AppDetails(ctx context.Context, appid int, country, lang string) (Game, error) {
	q := url.Values{}
	q.Set("appids", strconv.Itoa(appid))
	if country != "" {
		q.Set("cc", country)
	}
	if lang != "" {
		q.Set("l", lang)
	}
	rawURL := c.StoreBase + "/api/appdetails?" + q.Encode()

	body, err := c.doGet(ctx, rawURL)
	if err != nil {
		return Game{}, err
	}

	var top map[string]struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		return Game{}, fmt.Errorf("steam: decoding appdetails: %w", err)
	}
	entry, ok := top[strconv.Itoa(appid)]
	if !ok || !entry.Success || len(entry.Data) == 0 || string(entry.Data) == "null" {
		return Game{}, fmt.Errorf("steam: appdetails for %d: %w", appid, ErrNotFound)
	}

	var raw rawAppDetails
	if err := json.Unmarshal(entry.Data, &raw); err != nil {
		return Game{}, fmt.Errorf("steam: decoding appdetails data: %w", err)
	}

	return raw.toGame(), nil
}

// rawDescribedThing is one genre or category. Steam sends the id as a bare
// number, so it is not decoded at all: the description is the only part that
// is ever shown, and a field no one reads must not be able to fail a decode.
type rawDescribedThing struct {
	Description string `json:"description"`
}

// Steam is not consistent about JSON types: a metascore arrives as "" when a
// game has none and as a bare number when it has one, and app ids have been
// seen as both. These two types accept either spelling, so a field nobody needs
// cannot fail the decode of a response somebody does need.

// flexString decodes a JSON string, number, or null into a string.
type flexString string

func (f *flexString) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*f = ""
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*f = flexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	*f = flexString(n.String())
	return nil
}

// flexInt decodes a JSON number, numeric string, or null into an int.
type flexInt int

func (f *flexInt) UnmarshalJSON(data []byte) error {
	var s flexString
	if err := s.UnmarshalJSON(data); err != nil {
		return err
	}
	if s == "" {
		*f = 0
		return nil
	}
	n, err := strconv.Atoi(string(s))
	if err != nil {
		return err
	}
	*f = flexInt(n)
	return nil
}

type rawReleaseDate struct {
	ComingSoon bool   `json:"coming_soon"`
	Date       string `json:"date"`
}

type rawPlatforms struct {
	Windows bool `json:"windows"`
	Mac     bool `json:"mac"`
	Linux   bool `json:"linux"`
}

type rawPriceOverview struct {
	Currency        string `json:"currency"`
	Initial         int64  `json:"initial"`
	Final           int64  `json:"final"`
	DiscountPercent int    `json:"discount_percent"`
	FinalFormatted  string `json:"final_formatted"`
}

type rawMetacritic struct {
	Score int    `json:"score"`
	URL   string `json:"url"`
}

type rawRecommendations struct {
	Total int `json:"total"`
}

type rawAppDetails struct {
	SteamAppID       int                 `json:"steam_appid"`
	Name             string              `json:"name"`
	Type             string              `json:"type"`
	IsFree           bool                `json:"is_free"`
	HeaderImage      string              `json:"header_image"`
	CapsuleImage     string              `json:"capsule_image"`
	ShortDescription string              `json:"short_description"`
	Developers       []string            `json:"developers"`
	Publishers       []string            `json:"publishers"`
	Genres           []rawDescribedThing `json:"genres"`
	Categories       []rawDescribedThing `json:"categories"`
	ReleaseDate      rawReleaseDate      `json:"release_date"`
	Platforms        rawPlatforms        `json:"platforms"`
	PriceOverview    *rawPriceOverview   `json:"price_overview"`
	Metacritic       *rawMetacritic      `json:"metacritic"`
	Recommendations  *rawRecommendations `json:"recommendations"`
}

func (r rawAppDetails) toGame() Game {
	g := Game{
		AppID:            r.SteamAppID,
		Name:             r.Name,
		Type:             r.Type,
		IsFree:           r.IsFree,
		HeaderImage:      r.HeaderImage,
		CapsuleImage:     r.CapsuleImage,
		ShortDescription: r.ShortDescription,
		Developers:       r.Developers,
		Publishers:       r.Publishers,
		ReleaseDate:      r.ReleaseDate.Date,
		ComingSoon:       r.ReleaseDate.ComingSoon,
	}
	for _, gi := range r.Genres {
		g.Genres = append(g.Genres, gi.Description)
	}
	for _, ci := range r.Categories {
		g.Categories = append(g.Categories, ci.Description)
	}
	if r.Platforms.Windows {
		g.Platforms = append(g.Platforms, "windows")
	}
	if r.Platforms.Mac {
		g.Platforms = append(g.Platforms, "mac")
	}
	if r.Platforms.Linux {
		g.Platforms = append(g.Platforms, "linux")
	}
	if r.PriceOverview != nil {
		g.Price = &Price{
			Currency:        r.PriceOverview.Currency,
			Initial:         r.PriceOverview.Initial,
			Final:           r.PriceOverview.Final,
			DiscountPercent: r.PriceOverview.DiscountPercent,
			FormattedFinal:  r.PriceOverview.FinalFormatted,
		}
	}
	if r.Metacritic != nil {
		g.Metacritic = &Metacritic{Score: r.Metacritic.Score, URL: r.Metacritic.URL}
	}
	if r.Recommendations != nil {
		g.Recommendations = r.Recommendations.Total
	}
	return g
}

// Search queries the store search endpoint. country like "US", lang like
// "english".
func (c *Client) Search(ctx context.Context, term, country, lang string) ([]SearchResult, error) {
	q := url.Values{}
	q.Set("term", term)
	if country != "" {
		q.Set("cc", country)
	}
	if lang != "" {
		q.Set("l", lang)
	}
	rawURL := c.StoreBase + "/api/storesearch/?" + q.Encode()

	body, err := c.doGet(ctx, rawURL)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Total int `json:"total"`
		Items []struct {
			Type      string     `json:"type"`
			Name      string     `json:"name"`
			ID        flexInt    `json:"id"`
			TinyImage string     `json:"tiny_image"`
			Metascore flexString `json:"metascore"`
			Price     *struct {
				Currency string `json:"currency"`
				Initial  int64  `json:"initial"`
				Final    int64  `json:"final"`
			} `json:"price"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("steam: decoding storesearch: %w", err)
	}

	results := make([]SearchResult, 0, len(resp.Items))
	for _, it := range resp.Items {
		if it.Type != "app" {
			continue
		}
		sr := SearchResult{
			AppID:     int(it.ID),
			Name:      it.Name,
			TinyImage: it.TinyImage,
			Metascore: string(it.Metascore),
		}
		if it.Price != nil {
			sr.Currency = it.Price.Currency
			sr.PriceFinal = it.Price.Final
			sr.PriceInitial = it.Price.Initial
		}
		results = append(results, sr)
	}
	return results, nil
}

// Reviews fetches the aggregate review summary for appid.
func (c *Client) Reviews(ctx context.Context, appid int) (Reviews, error) {
	q := url.Values{}
	q.Set("json", "1")
	q.Set("language", "all")
	q.Set("purchase_type", "all")
	q.Set("num_per_page", "0")
	rawURL := c.StoreBase + "/appreviews/" + strconv.Itoa(appid) + "?" + q.Encode()

	body, err := c.doGet(ctx, rawURL)
	if err != nil {
		return Reviews{}, err
	}

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return Reviews{}, fmt.Errorf("steam: appreviews for %d returned non-JSON body: %w", appid, ErrNotFound)
	}

	var resp struct {
		Success      int `json:"success"`
		QuerySummary struct {
			TotalPositive   int    `json:"total_positive"`
			TotalNegative   int    `json:"total_negative"`
			TotalReviews    int    `json:"total_reviews"`
			ReviewScoreDesc string `json:"review_score_desc"`
		} `json:"query_summary"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return Reviews{}, fmt.Errorf("steam: appreviews for %d returned non-JSON body: %w", appid, ErrNotFound)
	}

	if resp.QuerySummary.TotalReviews == 0 {
		return Reviews{}, fmt.Errorf("steam: appreviews for %d: %w", appid, ErrNotFound)
	}

	return Reviews{
		TotalPositive: resp.QuerySummary.TotalPositive,
		TotalNegative: resp.QuerySummary.TotalNegative,
		TotalReviews:  resp.QuerySummary.TotalReviews,
		Description:   resp.QuerySummary.ReviewScoreDesc,
	}, nil
}

// PlayerSummary fetches a public profile summary for steamID. Requires
// APIKey.
func (c *Client) PlayerSummary(ctx context.Context, steamID string) (Player, error) {
	if c.APIKey == "" {
		return Player{}, ErrNoAPIKey
	}
	q := url.Values{}
	q.Set("key", c.APIKey)
	q.Set("steamids", steamID)
	rawURL := c.APIBase + "/ISteamUser/GetPlayerSummaries/v2/?" + q.Encode()

	body, err := c.doGet(ctx, rawURL)
	if err != nil {
		return Player{}, err
	}

	var resp struct {
		Response struct {
			Players []struct {
				SteamID     string `json:"steamid"`
				PersonaName string `json:"personaname"`
				Avatar      string `json:"avatar"`
				ProfileURL  string `json:"profileurl"`
			} `json:"players"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return Player{}, fmt.Errorf("steam: decoding GetPlayerSummaries: %w", err)
	}
	if len(resp.Response.Players) == 0 {
		return Player{}, fmt.Errorf("steam: player %s: %w", steamID, ErrNotFound)
	}
	p := resp.Response.Players[0]
	return Player{
		SteamID:     p.SteamID,
		PersonaName: p.PersonaName,
		Avatar:      p.Avatar,
		ProfileURL:  p.ProfileURL,
	}, nil
}

// OwnedGames returns the app ids in the player's public library. If the
// profile has no public game details, returns ErrNotFound. Requires APIKey.
func (c *Client) OwnedGames(ctx context.Context, steamID string) ([]int, error) {
	if c.APIKey == "" {
		return nil, ErrNoAPIKey
	}
	q := url.Values{}
	q.Set("key", c.APIKey)
	q.Set("steamid", steamID)
	q.Set("include_appinfo", "0")
	q.Set("include_played_free_games", "1")
	q.Set("include_free_sub", "0")
	rawURL := c.APIBase + "/IPlayerService/GetOwnedGames/v1/?" + q.Encode()

	body, err := c.doGet(ctx, rawURL)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Response struct {
			GameCount int `json:"game_count"`
			Games     *[]struct {
				AppID int `json:"appid"`
			} `json:"games"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("steam: decoding GetOwnedGames: %w", err)
	}
	if resp.Response.Games == nil {
		return nil, fmt.Errorf("steam: owned games for %s: %w", steamID, ErrNotFound)
	}

	appids := make([]int, 0, len(*resp.Response.Games))
	for _, g := range *resp.Response.Games {
		appids = append(appids, g.AppID)
	}
	return appids, nil
}
