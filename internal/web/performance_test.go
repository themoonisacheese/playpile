package web

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"playpile/internal/catalog"
	"playpile/internal/steam"
	"playpile/internal/store"
	"sync/atomic"
	"testing"
	"time"
)

type delayedReviews struct{ calls atomic.Int64 }

func (f *delayedReviews) AppDetails(context.Context, int, string, string) (steam.Game, error) {
	return steam.Game{}, steam.ErrNotFound
}
func (f *delayedReviews) Search(context.Context, string, string, string) ([]steam.SearchResult, error) {
	return nil, nil
}
func (f *delayedReviews) Reviews(ctx context.Context, id int) (steam.Reviews, error) {
	f.calls.Add(1)
	select {
	case <-time.After(15 * time.Millisecond):
		return steam.Reviews{}, steam.ErrNotFound
	case <-ctx.Done():
		return steam.Reviews{}, ctx.Err()
	}
}

func TestClickLatency(t *testing.T) {
	for _, count := range []int{1, 20} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			user, err := st.UpsertSteamUser("perf", "Perf", "", "")
			if err != nil {
				t.Fatal(err)
			}
			crew, err := st.CreateCrew("Perf", "US", "english", user.ID)
			if err != nil {
				t.Fatal(err)
			}
			var entry int64
			for id := 1; id <= count; id++ {
				row, err := st.AddEntry(crew.ID, id, user.ID)
				if err != nil {
					t.Fatal(err)
				}
				entry = row.ID
				if err := st.PutGameCache(id, "US", fmt.Sprintf(`{"AppID":%d,"Name":"Game %d","Price":{"Currency":"USD","Final":100}}`, id, id), time.Now().Unix()); err != nil {
					t.Fatal(err)
				}
			}
			upstream := &delayedReviews{}
			cat := catalog.New(st, upstream)
			server, err := New(Config{DevLogin: true}, st, cat, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			ts := httptest.NewServer(server.Handler())
			defer ts.Close()
			c := newClient(t)
			resp, _ := do(t, c, "POST", ts.URL+"/dev-login", url.Values{"name": {"Perf"}, "steamid": {"perf"}}, nil)
			if resp.StatusCode != 303 {
				t.Fatal(resp.StatusCode)
			}
			target := fmt.Sprintf("%s/c/%d", ts.URL, crew.ID)
			token := csrfFrom(t, c, target)
			for i := 0; i < 3; i++ {
				start := time.Now()
				resp, _ := do(t, c, "GET", target, nil, nil)
				get := time.Since(start)
				if resp.StatusCode != 200 {
					t.Fatal(resp.StatusCode)
				}
				start = time.Now()
				resp, body := do(t, c, "POST", fmt.Sprintf("%s/entries/%d/vote", target, entry), url.Values{"value": {"1"}, "csrf": {token}}, map[string]string{"X-Playpile": "fetch"})
				vote := time.Since(start)
				if resp.StatusCode != 200 {
					t.Fatalf("vote: %d %s", resp.StatusCode, body)
				}
				t.Logf("games=%d sample=%d GET=%s vote=%s upstream_calls=%d", count, i, get, vote, upstream.calls.Load())
			}
		})
	}
}
