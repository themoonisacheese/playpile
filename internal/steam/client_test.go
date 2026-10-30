package steam

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(storeURL, apiURL string) *Client {
	c := New("")
	c.StoreBase = storeURL
	c.APIBase = apiURL
	c.MinInterval = time.Millisecond
	c.HTTP = &http.Client{Timeout: 5 * time.Second}
	return c
}

const appDetailsOK = `{"730":{"success":true,"data":{
	"steam_appid":730,
	"name":"Counter-Strike 2",
	"type":"game",
	"is_free":false,
	"header_image":"https://example.com/header.jpg",
	"capsule_image":"https://example.com/capsule.jpg",
	"short_description":"A tactical shooter.",
	"developers":["Valve"],
	"publishers":["Valve"],
	"genres":[{"id":"1","description":"Action"},{"id":"37","description":"Free to Play"}],
	"categories":[{"id":"1","description":"Multi-player"},{"id":"49","description":"PvP"}],
	"release_date":{"coming_soon":false,"date":"21 Aug, 2012"},
	"platforms":{"windows":true,"mac":false,"linux":true},
	"price_overview":{"currency":"PLN","initial":19900,"final":9900,"discount_percent":50,"final_formatted":"99,00 zl"},
	"metacritic":{"score":83,"url":"https://metacritic.example/cs2"},
	"recommendations":{"total":240937}
}}}`

func TestAppDetailsHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/appdetails" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(appDetailsOK))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, srv.URL)
	g, err := c.AppDetails(context.Background(), 730, "US", "english")
	if err != nil {
		t.Fatalf("AppDetails: %v", err)
	}
	if g.AppID != 730 || g.Name != "Counter-Strike 2" || g.Type != "game" {
		t.Errorf("basic fields wrong: %+v", g)
	}
	if g.IsFree {
		t.Errorf("expected IsFree=false")
	}
	if len(g.Genres) != 2 || g.Genres[0] != "Action" || g.Genres[1] != "Free to Play" {
		t.Errorf("genres wrong: %v", g.Genres)
	}
	if len(g.Categories) != 2 || g.Categories[0] != "Multi-player" || g.Categories[1] != "PvP" {
		t.Errorf("categories wrong: %v", g.Categories)
	}
	if g.ReleaseDate != "21 Aug, 2012" || g.ComingSoon {
		t.Errorf("release date wrong: %q coming_soon=%v", g.ReleaseDate, g.ComingSoon)
	}
	if len(g.Platforms) != 2 || g.Platforms[0] != "windows" || g.Platforms[1] != "linux" {
		t.Errorf("platforms wrong: %v", g.Platforms)
	}
	if g.Price == nil {
		t.Fatalf("expected non-nil price")
	}
	if g.Price.Currency != "PLN" || g.Price.Initial != 19900 || g.Price.Final != 9900 || g.Price.DiscountPercent != 50 || g.Price.FormattedFinal != "99,00 zl" {
		t.Errorf("price wrong: %+v", g.Price)
	}
	if g.Metacritic == nil || g.Metacritic.Score != 83 {
		t.Errorf("metacritic wrong: %+v", g.Metacritic)
	}
	if g.Recommendations != 240937 {
		t.Errorf("recommendations wrong: %d", g.Recommendations)
	}
}

func TestAppDetailsSuccessFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"1":{"success":false}}`))
	}))
	defer srv.Close()
	c := newTestClient(srv.URL, srv.URL)
	_, err := c.AppDetails(context.Background(), 1, "US", "english")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestAppDetailsDataMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"1":{"success":true}}`))
	}))
	defer srv.Close()
	c := newTestClient(srv.URL, srv.URL)
	_, err := c.AppDetails(context.Background(), 1, "US", "english")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestAppDetailsFreeNoPrice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"440":{"success":true,"data":{
			"steam_appid":440,"name":"Team Fortress 2","type":"game","is_free":true,
			"platforms":{"windows":true,"mac":true,"linux":true}
		}}}`))
	}))
	defer srv.Close()
	c := newTestClient(srv.URL, srv.URL)
	g, err := c.AppDetails(context.Background(), 440, "US", "english")
	if err != nil {
		t.Fatalf("AppDetails: %v", err)
	}
	if !g.IsFree {
		t.Errorf("expected IsFree=true")
	}
	if g.Price != nil {
		t.Errorf("expected nil price, got %+v", g.Price)
	}
}

func TestSearch(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Write([]byte(`{"total":2,"items":[
			{"type":"app","name":"Half-Life","id":70,"price":{"currency":"USD","initial":999,"final":999},"tiny_image":"https://img/70.jpg","metascore":"96"},
			{"type":"bundle","name":"Some Bundle","id":99},
			{"type":"app","name":"Half-Life 2","id":220,"tiny_image":"https://img/220.jpg","metascore":""}
		]}`))
	}))
	defer srv.Close()
	c := newTestClient(srv.URL, srv.URL)
	results, err := c.Search(context.Background(), "half life", "US", "english")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results (bundle filtered out), got %d: %+v", len(results), results)
	}
	if results[0].AppID != 70 || results[0].Currency != "USD" || results[0].PriceFinal != 999 || results[0].PriceInitial != 999 || results[0].Metascore != "96" {
		t.Errorf("first result wrong: %+v", results[0])
	}
	if results[1].AppID != 220 || results[1].PriceFinal != 0 || results[1].Metascore != "" {
		t.Errorf("second result wrong: %+v", results[1])
	}
	if gotQuery.Get("term") != "half life" {
		t.Errorf("expected term to decode back to 'half life', got %q (raw query %q)", gotQuery.Get("term"), gotQuery.Encode())
	}
}

func TestReviewsHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":1,"query_summary":{"num_reviews":5,"review_score":8,"review_score_desc":"Very Positive","total_positive":240100,"total_negative":9000,"total_reviews":249100}}`))
	}))
	defer srv.Close()
	c := newTestClient(srv.URL, srv.URL)
	rv, err := c.Reviews(context.Background(), 730)
	if err != nil {
		t.Fatalf("Reviews: %v", err)
	}
	if rv.TotalPositive != 240100 || rv.TotalNegative != 9000 || rv.TotalReviews != 249100 || rv.Description != "Very Positive" {
		t.Errorf("reviews wrong: %+v", rv)
	}
}

func TestReviewsHTMLBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body>not json</body></html>`))
	}))
	defer srv.Close()
	c := newTestClient(srv.URL, srv.URL)
	_, err := c.Reviews(context.Background(), 730)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestReviewsZeroTotal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":1,"query_summary":{"total_reviews":0}}`))
	}))
	defer srv.Close()
	c := newTestClient(srv.URL, srv.URL)
	_, err := c.Reviews(context.Background(), 1)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPlayerSummaryHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "k3y" {
			t.Errorf("missing key param")
		}
		w.Write([]byte(`{"response":{"players":[{"steamid":"1","personaname":"Bob","avatar":"https://a.jpg","profileurl":"https://p"}]}}`))
	}))
	defer srv.Close()
	c := newTestClient(srv.URL, srv.URL)
	c.APIKey = "k3y"
	p, err := c.PlayerSummary(context.Background(), "1")
	if err != nil {
		t.Fatalf("PlayerSummary: %v", err)
	}
	if p.SteamID != "1" || p.PersonaName != "Bob" || p.Avatar != "https://a.jpg" || p.ProfileURL != "https://p" {
		t.Errorf("player wrong: %+v", p)
	}
}

func TestPlayerSummaryNoAPIKey(t *testing.T) {
	c := newTestClient("http://unused.invalid", "http://unused.invalid")
	_, err := c.PlayerSummary(context.Background(), "1")
	if !errors.Is(err, ErrNoAPIKey) {
		t.Fatalf("expected ErrNoAPIKey, got %v", err)
	}
}

func TestPlayerSummaryEmptyPlayers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"response":{"players":[]}}`))
	}))
	defer srv.Close()
	c := newTestClient(srv.URL, srv.URL)
	c.APIKey = "k"
	_, err := c.PlayerSummary(context.Background(), "1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestOwnedGamesHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"response":{"game_count":2,"games":[{"appid":440},{"appid":730}]}}`))
	}))
	defer srv.Close()
	c := newTestClient(srv.URL, srv.URL)
	c.APIKey = "k"
	ids, err := c.OwnedGames(context.Background(), "1")
	if err != nil {
		t.Fatalf("OwnedGames: %v", err)
	}
	if len(ids) != 2 || ids[0] != 440 || ids[1] != 730 {
		t.Errorf("ids wrong: %v", ids)
	}
}

func TestOwnedGamesMissingGames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"response":{"game_count":0}}`))
	}))
	defer srv.Close()
	c := newTestClient(srv.URL, srv.URL)
	c.APIKey = "k"
	_, err := c.OwnedGames(context.Background(), "1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestOwnedGamesNoAPIKey(t *testing.T) {
	c := newTestClient("http://unused.invalid", "http://unused.invalid")
	_, err := c.OwnedGames(context.Background(), "1")
	if !errors.Is(err, ErrNoAPIKey) {
		t.Fatalf("expected ErrNoAPIKey, got %v", err)
	}
}

func TestRetryOn503ThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(appDetailsOK))
	}))
	defer srv.Close()
	c := newTestClient(srv.URL, srv.URL)
	g, err := c.AppDetails(context.Background(), 730, "US", "english")
	if err != nil {
		t.Fatalf("AppDetails: %v", err)
	}
	if g.AppID != 730 {
		t.Errorf("wrong appid after retry: %d", g.AppID)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("expected 2 calls, got %d", calls)
	}
}

func TestNoRetryOn400(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	c := newTestClient(srv.URL, srv.URL)
	_, err := c.AppDetails(context.Background(), 730, "US", "english")
	if err == nil {
		t.Fatalf("expected error")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("expected exactly 1 call (no retry on 400), got %d", calls)
	}
}

func TestContextCancellation(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer func() {
		close(block)
		srv.Close()
	}()
	c := newTestClient(srv.URL, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.AppDetails(ctx, 730, "US", "english")
	if err == nil {
		t.Fatalf("expected error from cancelled context")
	}
}

func TestThrottleSpacing(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Write([]byte(appDetailsOK))
	}))
	defer srv.Close()
	c := newTestClient(srv.URL, srv.URL)
	c.MinInterval = 30 * time.Millisecond
	start := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := c.AppDetails(context.Background(), 730, "US", "english"); err != nil {
			t.Fatalf("AppDetails: %v", err)
		}
	}
	elapsed := time.Since(start)
	if elapsed < 60*time.Millisecond {
		t.Errorf("expected throttling to space out calls, elapsed=%v", elapsed)
	}
}

func ExampleClient_errorsIsNotFound() {
	err := fmt.Errorf("wrapped: %w", ErrNotFound)
	fmt.Println(errors.Is(err, ErrNotFound))
	// Output: true
}
