package catalog

import (
	"context"
	"fmt"
	"playpile/internal/steam"
	"playpile/internal/store"
	"sync/atomic"
	"testing"
	"time"
)

type blockedSteam struct {
	release chan struct{}
	calls   atomic.Int64
}

func (b *blockedSteam) AppDetails(ctx context.Context, id int, country, lang string) (steam.Game, error) {
	b.calls.Add(1)
	select {
	case <-b.release:
		return steam.Game{}, steam.ErrNotFound
	case <-ctx.Done():
		return steam.Game{}, ctx.Err()
	}
}
func (b *blockedSteam) Reviews(ctx context.Context, id int) (steam.Reviews, error) {
	b.calls.Add(1)
	select {
	case <-b.release:
		return steam.Reviews{}, steam.ErrNotFound
	case <-ctx.Done():
		return steam.Reviews{}, ctx.Err()
	}
}
func (b *blockedSteam) Search(context.Context, string, string, string) ([]steam.SearchResult, error) {
	return nil, nil
}

func waitBackground(t *testing.T, c *Catalog) {
	t.Helper()
	deadline := time.After(time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if len(c.background) == 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("background did not finish")
		case <-tick.C:
		}
	}
}

func TestDisplayNeverWaitsForSteam(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	upstream := &blockedSteam{release: make(chan struct{})}
	c := New(st, upstream)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for id := 1; id <= 200; id++ {
			_, _ = c.CachedGame(context.Background(), id, "US", "english")
			_, _ = c.CachedReviews(context.Background(), id)
		}
	}()
	select {
	case <-finished:
	case <-time.After(time.Second):
		close(upstream.release)
		t.Fatal("display waited for upstream")
	}
	if got := len(c.background); got != 2 {
		t.Errorf("background jobs=%d, want 2", got)
	}
	c.mu.Lock()
	keys := len(c.refreshing)
	c.mu.Unlock()
	if keys > 2 {
		t.Errorf("queued %d keys while blocked", keys)
	}
	close(upstream.release)
	waitBackground(t, c)
	calls := upstream.calls.Load()
	for id := 1; id <= 1; id++ {
		_, _ = c.CachedGame(context.Background(), id, "US", "english")
		_, _ = c.CachedReviews(context.Background(), id)
	}
	waitBackground(t, c)
	// The first game's two keys were admitted before saturation; retries must back off.
	c.mu.Lock()
	state := c.refreshing["game:US:1"]
	c.mu.Unlock()
	if state.running || state.retryAfter.IsZero() {
		t.Fatal("missing failed-request cooldown")
	}
	if calls != 2 || upstream.calls.Load() != calls {
		t.Errorf("failed requests retried during cooldown: before=%d after=%d", calls, upstream.calls.Load())
	}
}

func TestRefreshBookkeepingBoundAndExpiry(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := New(st, &blockedSteam{})
	now := time.Now()
	c.Now = func() time.Time { return now }
	for i := 0; i < maxRefreshKeys; i++ {
		c.refreshing[fmt.Sprint(i)] = refreshState{retryAfter: now.Add(time.Minute)}
	}
	var calls atomic.Int64
	c.revalidate("new", func(context.Context) { calls.Add(1) })
	if calls.Load() != 0 || len(c.refreshing) != maxRefreshKeys {
		t.Fatal("refresh exceeded bookkeeping cap")
	}
	now = now.Add(2 * time.Minute)
	c.revalidate("new", func(context.Context) { calls.Add(1) })
	waitBackground(t, c)
	c.mu.Lock()
	keys := len(c.refreshing)
	c.mu.Unlock()
	if calls.Load() != 1 || keys != 1 {
		t.Fatal("expired cooldowns not pruned")
	}
}

func TestCachedDataPreservedDuringRefreshFailure(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	b := &blockedSteam{release: make(chan struct{})}
	close(b.release)
	c := New(st, b)
	if err := st.PutGameCache(1, "US", `{"AppID":1,"Name":"Saved game","Price":{"Currency":"USD","Final":123}}`, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.PutReviewCache(1, `{"total_reviews":100,"total_positive":90}`, 1); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		game, err := c.CachedGame(context.Background(), 1, "US", "english")
		if err != nil || game.Name != "Saved game" || game.Price.Final != 123 {
			t.Fatalf("game lost: %+v %v", game, err)
		}
		review, err := c.CachedReviews(context.Background(), 1)
		if err != nil || review.TotalReviews != 100 {
			t.Fatalf("reviews lost: %+v %v", review, err)
		}
		waitBackground(t, c)
	}
}
