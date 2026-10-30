// Package pile assembles one crew's game list into the view model the pages
// render: entries with metadata, votes, ownership, and the three money totals.
package pile

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"playpile/internal/catalog"
	"playpile/internal/steam"
	"playpile/internal/store"
)

// Filters understood by the list view.
const (
	FilterActive  = ""
	FilterPlayed  = "played"
	FilterMissing = "missing"
	FilterReady   = "ready"
	FilterLiked   = "liked"
)

// Sorts understood by the list view.
const (
	SortScore = "score"
	SortAdded = "added"
	SortPrice = "price"
	SortName  = "name"
	SortMeta  = "metacritic"
)

// Options are the user's current list view settings.
type Options struct {
	Filter string
	Sort   string
}

// Normalize legalises filter/sort strings from a query string.
func (o *Options) Normalize() {
	switch o.Filter {
	case FilterPlayed, FilterMissing, FilterReady, FilterLiked, FilterActive:
	default:
		o.Filter = FilterActive
	}
	switch o.Sort {
	case SortAdded, SortPrice, SortName, SortMeta, SortScore:
	default:
		o.Sort = SortScore
	}
}

// Entry is one game on the list, ready to render.
type Entry struct {
	store.Entry
	Game      steam.Game
	Reviews   *steam.Reviews
	Available bool // Steam metadata was found

	Price *steam.Price // nil when free or not priced in the crew's region
	// Currency is the crew's display currency when Price is usable, else "".
	Currency string

	Owners    []store.Member // members who own it, in member order
	OwnedByMe bool
	Missing   int   // how many members do not own it
	CostForMe int64 // minor units this entry would cost the viewer

	AddedByName string
	FetchedAt   int64 // newest metadata timestamp for this entry
}

// Score is the net vote count.
func (e Entry) Score() int { return e.Up - e.Down }

// Vetoed reports whether anyone has cast a hard no. One hard no is enough:
// a vetoed game is disqualified from the list that everyone can pick from, is
// left out of the money totals, and always sinks to the bottom of the page,
// whatever the chosen sort.
func (e Entry) Vetoed() bool { return e.Down > 0 }

// Free reports whether the game costs nothing in this region.
func (e Entry) Free() bool { return e.Game.IsFree || (e.Price != nil && e.Price.Final == 0) }

// OwnedByEverybody reports whether the whole crew can play it without buying
// anything: either everybody owns it, or it costs nothing.
func (e Entry) OwnedByEverybody() bool { return e.Free() || e.Missing == 0 }

// ReviewPercent is Steam's positive-review share, 0 when unknown.
func (e Entry) ReviewPercent() int {
	if e.Reviews == nil || e.Reviews.TotalReviews == 0 {
		return 0
	}
	return int(float64(e.Reviews.TotalPositive)/float64(e.Reviews.TotalReviews)*100 + 0.5)
}

// Summary is the at-a-glance strip above the list.
type Summary struct {
	Currency string

	Total     int64 // the whole list, one copy of each game
	MyCost    int64 // what the viewer would still have to spend
	GroupCost int64 // what everyone together would have to spend

	Priced       int // entries counted in the totals
	Free         int // entries that cost nothing
	Unpriced     int // entries with no price in this region
	OtherMoney   int // entries priced in a different currency (excluded)
	Ready        int // entries every member already owns
	MissingForMe int // priced entries the viewer does not own
	Played       int // entries moved to "played"
	Upvotes      int // total up-votes cast across the list
	Vetoed       int // entries disqualified by at least one hard no

	MemberCount int
}

// Pile is everything a list page needs.
type Pile struct {
	Crew    store.Crew
	Me      store.Member
	Members []store.Member

	// Entries is the filtered, sorted view the page renders.
	Entries []Entry
	// All is every entry in the crew, before filtering.
	All []Entry

	Summary Summary
	Options Options

	// DataAge is the timestamp of the oldest metadata shown, for the
	// "prices fetched N hours ago" hint. 0 when nothing was fetched.
	DataAge int64
}

// IsMember reports whether the viewer is at least a member.
func (p *Pile) IsMember() bool { return store.Rank(p.Me.Role) >= store.Rank(store.RoleMember) }

// IsAdmin reports whether the viewer may manage members and settings.
func (p *Pile) IsAdmin() bool { return store.Rank(p.Me.Role) >= store.Rank(store.RoleAdmin) }

// IsOwner reports whether the viewer owns the crew.
func (p *Pile) IsOwner() bool { return p.Me.Role == store.RoleOwner }

// Counts returns how many entries each filter would show, for the toolbar.
func (p *Pile) Counts() map[string]int {
	counts := map[string]int{FilterActive: 0, FilterPlayed: 0, FilterMissing: 0, FilterReady: 0, FilterLiked: 0}
	for _, e := range p.All {
		if e.Status == store.StatusPlayed {
			counts[FilterPlayed]++
			continue
		}
		counts[FilterActive]++
		if e.Vetoed() {
			// A hard no disqualifies the game: it is not something to buy,
			// and it is not something you are still campaigning for.
			continue
		}
		if e.OwnedByEverybody() {
			counts[FilterReady]++
		}
		if e.CostForMe > 0 {
			counts[FilterMissing]++
		}
		if e.MyVote > 0 {
			counts[FilterLiked]++
		}
	}
	return counts
}

// Build loads a crew's list and computes every derived number.
func Build(ctx context.Context, st *store.Store, cat *catalog.Catalog, crew store.Crew, me store.User, opts Options) (*Pile, error) {
	opts.Normalize()

	members, err := st.Members(crew.ID)
	if err != nil {
		return nil, err
	}
	role := store.RoleMember
	for _, m := range members {
		if m.ID == me.ID {
			role = m.Role
		}
	}

	raw, err := st.Entries(crew.ID, me.ID)
	if err != nil {
		return nil, err
	}
	ownership, err := st.CrewOwnership(crew.ID)
	if err != nil {
		return nil, err
	}

	entries := fetchMetadata(ctx, st, cat, crew, raw)

	// Ownership and per-entry money.
	for i := range entries {
		e := &entries[i]
		owned := ownership[e.AppID]
		e.OwnedByMe = owned[me.ID]
		missing := 0
		var owners []store.Member
		for _, m := range members {
			if owned[m.ID] {
				owners = append(owners, m)
			} else {
				missing++
			}
		}
		e.Owners = owners
		e.Missing = missing
	}

	p := &Pile{Crew: crew, Me: store.Member{User: me, Role: role}, Members: members, All: entries, Options: opts}
	p.Summary = summarise(entries, members, me.ID)
	p.Entries = applyFilter(entries, opts.Filter)
	sortEntries(p.Entries, opts.Sort)
	p.DataAge = oldestFetch(entries)

	return p, nil
}

// fetchMetadata loads Steam metadata for every entry, with a little
// concurrency so a first view of a long list is not a chain of round trips.
func fetchMetadata(ctx context.Context, st *store.Store, cat *catalog.Catalog, crew store.Crew, raw []store.Entry) []Entry {
	entries := make([]Entry, len(raw))
	if len(raw) == 0 {
		return entries
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	now := time.Now().Unix()

	for i, r := range raw {
		entries[i] = Entry{Entry: r}
		wg.Add(1)
		go func(i int, r store.Entry) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			g, err := cat.Game(ctx, r.AppID, crew.Country, crew.Language)
			if err == nil && g.Name != "" {
				entries[i].Game = g
				entries[i].Price = g.Price
				entries[i].Available = true
				entries[i].FetchedAt = now
				if cached, cacheErr := st.GetGameCache(r.AppID, crew.Country); cacheErr == nil {
					entries[i].FetchedAt = cached.FetchedAt
				}
			}
			if rev, err := cat.Reviews(ctx, r.AppID); err == nil {
				entries[i].Reviews = &rev
			}
		}(i, r)
	}
	wg.Wait()
	return entries
}

// summarise computes the strip: totals, readiness and counts.
func summarise(entries []Entry, members []store.Member, meID int64) Summary {
	s := Summary{MemberCount: 0}
	s.MemberCount = len(members)

	// The display currency is whichever currency most priced entries use.
	counts := make(map[string]int)
	for _, e := range entries {
		if e.Price != nil && e.Price.Currency != "" && e.Price.Final > 0 {
			counts[e.Price.Currency]++
		}
	}
	best, bestN := "", 0
	for cur, n := range counts {
		if n > bestN || (n == bestN && cur < best) {
			best, bestN = cur, n
		}
	}
	s.Currency = best

	for i := range entries {
		e := &entries[i]
		if e.Status == store.StatusPlayed {
			s.Played++
		}
		// A vetoed game is disqualified outright: it stays on the page, but it
		// counts for nothing — not for the totals, not for ready-to-play, not
		// for what you would have to buy.
		if e.Vetoed() {
			s.Vetoed++
			continue
		}
		s.Upvotes += e.Up
		if e.OwnedByEverybody() {
			s.Ready++
		}
		switch {
		case e.Free():
			s.Free++
			e.Currency = best
			continue
		case e.Price == nil || e.Price.Final <= 0:
			s.Unpriced++
			continue
		case best != "" && e.Price.Currency != best:
			s.OtherMoney++
			continue
		}
		e.Currency = best
		s.Priced++
		s.Total += e.Price.Final
		s.GroupCost += e.Price.Final * int64(e.Missing)
		if !e.OwnedByMe {
			s.MissingForMe++
			s.MyCost += e.Price.Final
			e.CostForMe = e.Price.Final
		}
	}
	return s
}

func applyFilter(entries []Entry, filter string) []Entry {
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		switch filter {
		case FilterPlayed:
			if e.Status != store.StatusPlayed {
				continue
			}
		case FilterMissing:
			if e.Status == store.StatusPlayed || e.Vetoed() || e.CostForMe <= 0 {
				continue
			}
		case FilterReady:
			if e.Status == store.StatusPlayed || e.Vetoed() || !e.OwnedByEverybody() {
				continue
			}
		case FilterLiked:
			if e.Status == store.StatusPlayed || e.Vetoed() || e.MyVote <= 0 {
				continue
			}
		default:
			if e.Status == store.StatusPlayed {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

func sortEntries(entries []Entry, order string) {
	byName := func(a, b Entry) bool { return strings.ToLower(a.Game.Name) < strings.ToLower(b.Game.Name) }
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		// A hard no disqualifies a game, so disqualified rows sink to the
		// bottom of the list whatever the chosen order.
		if a.Vetoed() != b.Vetoed() {
			return !a.Vetoed()
		}
		switch order {
		case SortAdded:
			return a.AddedAt > b.AddedAt
		case SortPrice:
			if a.priceOrZero() != b.priceOrZero() {
				return a.priceOrZero() > b.priceOrZero()
			}
			return byName(a, b)
		case SortName:
			return byName(a, b)
		case SortMeta:
			if a.metaOrZero() != b.metaOrZero() {
				return a.metaOrZero() > b.metaOrZero()
			}
			return byName(a, b)
		default: // score
			if a.Score() != b.Score() {
				return a.Score() > b.Score()
			}
			if a.AddedAt != b.AddedAt {
				return a.AddedAt < b.AddedAt
			}
			return byName(a, b)
		}
	})
}

func (e Entry) priceOrZero() int64 {
	if e.Price == nil {
		return 0
	}
	return e.Price.Final
}

func (e Entry) metaOrZero() int {
	if e.Game.Metacritic == nil {
		return 0
	}
	return e.Game.Metacritic.Score
}

func oldestFetch(entries []Entry) int64 {
	var oldest int64
	for _, e := range entries {
		if e.FetchedAt == 0 {
			continue
		}
		if oldest == 0 || e.FetchedAt < oldest {
			oldest = e.FetchedAt
		}
	}
	return oldest
}
