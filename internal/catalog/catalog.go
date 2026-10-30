// Package catalog caches Steam store metadata in SQLite and refreshes it in
// the background, so page loads never wait on Steam when a cached copy exists.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"playpile/internal/steam"
	"playpile/internal/store"
)

// Default time-to-live values. Prices move when a sale starts, which is why
// they are refreshed more eagerly than review summaries.
const (
	DefaultGameTTL   = 6 * time.Hour
	DefaultReviewTTL = 12 * time.Hour
)

// ErrUnavailable means Steam has no usable data for this app (removed,
// region-locked, or the reviews endpoint answered with HTML).
var ErrUnavailable = errors.New("catalog: unavailable")

// Client is the slice of the Steam client the catalog needs.
type Client interface {
	AppDetails(ctx context.Context, appid int, country, lang string) (steam.Game, error)
	Search(ctx context.Context, term, country, lang string) ([]steam.SearchResult, error)
	Reviews(ctx context.Context, appid int) (steam.Reviews, error)
}

// Catalog is a caching facade over the Steam client.
type Catalog struct {
	store *store.Store
	steam Client

	GameTTL   time.Duration
	ReviewTTL time.Duration
	Now       func() time.Time

	// limit caps how many upstream requests run at once; Steam dislikes bursts.
	limit chan struct{}

	mu         sync.Mutex
	refreshing map[string]bool
}

// New builds a catalog backed by st and cl.
func New(st *store.Store, cl Client) *Catalog {
	return &Catalog{
		store:      st,
		steam:      cl,
		GameTTL:    DefaultGameTTL,
		ReviewTTL:  DefaultReviewTTL,
		Now:        time.Now,
		limit:      make(chan struct{}, 4),
		refreshing: make(map[string]bool),
	}
}

// Game returns metadata for an app in a region, from cache when possible.
// Stale entries are served immediately and refreshed in the background.
func (c *Catalog) Game(ctx context.Context, appid int, country, lang string) (steam.Game, error) {
	cached, err := c.store.GetGameCache(appid, country)
	if err == nil {
		var g steam.Game
		if jsonErr := json.Unmarshal([]byte(cached.JSON), &g); jsonErr == nil && g.Name != "" {
			if c.stale(cached.FetchedAt, c.GameTTL) {
				c.revalidate("game:"+country+":"+itoa(appid), func(ctx context.Context) { _ = c.RefreshGame(ctx, appid, country, lang) })
			}
			return g, nil
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return steam.Game{}, err
	}
	return c.fetchGame(ctx, appid, country, lang)
}

// RefreshGame force-fetches an app from Steam and updates the cache.
func (c *Catalog) RefreshGame(ctx context.Context, appid int, country, lang string) error {
	_, err := c.fetchGame(ctx, appid, country, lang)
	return err
}

func (c *Catalog) fetchGame(ctx context.Context, appid int, country, lang string) (steam.Game, error) {
	if err := c.acquire(ctx); err != nil {
		return steam.Game{}, err
	}
	g, err := c.steam.AppDetails(ctx, appid, country, lang)
	c.release()
	if err != nil {
		if errors.Is(err, steam.ErrNotFound) {
			return steam.Game{}, ErrUnavailable
		}
		return steam.Game{}, err
	}
	if blob, err := json.Marshal(g); err == nil {
		_ = c.store.PutGameCache(appid, country, string(blob), c.Now().Unix())
	}
	return g, nil
}

// Reviews returns the review summary for an app, from cache when possible.
func (c *Catalog) Reviews(ctx context.Context, appid int) (steam.Reviews, error) {
	cached, err := c.store.GetReviewCache(appid)
	if err == nil {
		var r steam.Reviews
		if jsonErr := json.Unmarshal([]byte(cached.JSON), &r); jsonErr == nil && r.TotalReviews > 0 {
			if c.stale(cached.FetchedAt, c.ReviewTTL) {
				c.revalidate("reviews:"+itoa(appid), func(ctx context.Context) { _ = c.RefreshReviews(ctx, appid) })
			}
			return r, nil
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return steam.Reviews{}, err
	}
	return c.fetchReviews(ctx, appid)
}

// RefreshReviews force-fetches a review summary from Steam.
func (c *Catalog) RefreshReviews(ctx context.Context, appid int) error {
	_, err := c.fetchReviews(ctx, appid)
	return err
}

func (c *Catalog) fetchReviews(ctx context.Context, appid int) (steam.Reviews, error) {
	if err := c.acquire(ctx); err != nil {
		return steam.Reviews{}, err
	}
	r, err := c.steam.Reviews(ctx, appid)
	c.release()
	if err != nil {
		if errors.Is(err, steam.ErrNotFound) {
			return steam.Reviews{}, ErrUnavailable
		}
		return steam.Reviews{}, err
	}
	if blob, err := json.Marshal(r); err == nil {
		_ = c.store.PutReviewCache(appid, string(blob), c.Now().Unix())
	}
	return r, nil
}

// Search passes a title search through to Steam (no caching: searches are
// user-specific and cheap).
func (c *Catalog) Search(ctx context.Context, term, country, lang string) ([]steam.SearchResult, error) {
	if err := c.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.release()
	return c.steam.Search(ctx, term, country, lang)
}

// Invalidate drops the cached price/metadata for one app in one region, which
// is what "refresh" does for a single row.
func (c *Catalog) Invalidate(appid int, country string) {
	_ = c.store.PutGameCache(appid, country, "", 0)
}

// stale reports whether a timestamp is older than ttl.
func (c *Catalog) stale(fetchedAt int64, ttl time.Duration) bool {
	return c.Now().Unix()-fetchedAt > int64(ttl.Seconds())
}

// revalidate runs fn in the background unless the same key is already running.
func (c *Catalog) revalidate(key string, fn func(context.Context)) {
	c.mu.Lock()
	if c.refreshing[key] {
		c.mu.Unlock()
		return
	}
	c.refreshing[key] = true
	c.mu.Unlock()

	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.refreshing, key)
			c.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		fn(ctx)
	}()
}

func (c *Catalog) acquire(ctx context.Context) error {
	select {
	case c.limit <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Catalog) release() { <-c.limit }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [12]byte
	pos := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
