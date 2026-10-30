package pile

import (
	"context"
	"testing"

	"playpile/internal/catalog"
	"playpile/internal/steam"
	"playpile/internal/store"
)

type fakeClient struct {
	games   map[int]steam.Game
	reviews map[int]steam.Reviews
}

func (f *fakeClient) AppDetails(ctx context.Context, appid int, country, lang string) (steam.Game, error) {
	if g, ok := f.games[appid]; ok {
		return g, nil
	}
	return steam.Game{}, steam.ErrNotFound
}

func (f *fakeClient) Search(ctx context.Context, term, country, lang string) ([]steam.SearchResult, error) {
	return nil, nil
}

func (f *fakeClient) Reviews(ctx context.Context, appid int) (steam.Reviews, error) {
	if r, ok := f.reviews[appid]; ok {
		return r, nil
	}
	return steam.Reviews{}, steam.ErrNotFound
}

func price(currency string, cents int64) *steam.Price {
	return &steam.Price{Currency: currency, Initial: cents, Final: cents}
}

// fixture builds a crew of three with a handful of games and ownership.
func fixture(t *testing.T) (*store.Store, *catalog.Catalog, store.Crew, store.User, []store.Member) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ada, _ := st.UpsertSteamUser("1", "Ada", "", "")
	bo, _ := st.UpsertSteamUser("2", "Bo", "", "")
	cy, _ := st.UpsertSteamUser("3", "Cy", "", "")
	crew, _ := st.CreateCrew("Friday", "US", "english", ada.ID)

	inv, _ := st.CreateInvite(crew.ID, ada.ID, store.RoleMember, 0, 0)
	for _, u := range []store.User{bo, cy} {
		if _, err := st.RedeemInvite(inv.Token, u.ID); err != nil {
			t.Fatalf("invite: %v", err)
		}
	}

	fake := &fakeClient{
		games: map[int]steam.Game{
			10: {AppID: 10, Name: "Alpha", Price: price("USD", 1000), Metacritic: &steam.Metacritic{Score: 90},
				Developers: []string{"Studio"}, ReleaseDate: "3 Dec, 2019"},
			20: {AppID: 20, Name: "Beta", Price: price("USD", 2000)},
			30: {AppID: 30, Name: "Gamma", IsFree: true},
			40: {AppID: 40, Name: "Delta"},
			50: {AppID: 50, Name: "Epsilon", Price: price("EUR", 500)},
		},
		reviews: map[int]steam.Reviews{
			10: {TotalPositive: 900, TotalNegative: 100, TotalReviews: 1000, Description: "Very Positive"},
		},
	}
	cat := catalog.New(st, fake)

	for _, appid := range []int{10, 20, 30, 40, 50} {
		if _, err := st.AddEntry(crew.ID, appid, ada.ID); err != nil {
			t.Fatalf("add %d: %v", appid, err)
		}
	}
	// Ada and Bo own Alpha; Ada owns Beta too, so only Cy is missing it.
	_ = st.SetOwned(ada.ID, 10, true, store.SourceManual)
	_ = st.SetOwned(bo.ID, 10, true, store.SourceManual)
	_ = st.SetOwned(ada.ID, 20, true, store.SourceManual)

	members, err := st.Members(crew.ID)
	if err != nil {
		t.Fatalf("members: %v", err)
	}
	return st, cat, crew, ada, members
}

func TestBuildComputesTotals(t *testing.T) {
	st, cat, crew, ada, members := fixture(t)
	p, err := Build(context.Background(), st, cat, crew, ada, Options{})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if len(members) != 3 {
		t.Fatalf("members = %d", len(members))
	}
	// Alpha 10.00 (2 of 3 own it) and Beta 20.00 (1 of 3 owns it) are priced in
	// USD; Epsilon is EUR and must not pollute the totals.
	if p.Summary.Total != 3000 {
		t.Errorf("Total = %d, want 3000", p.Summary.Total)
	}
	if p.Summary.MyCost != 0 {
		t.Errorf("MyCost = %d, want 0 (Ada owns both priced games)", p.Summary.MyCost)
	}
	// Alpha: 1 member missing -> 10.00. Beta: 2 members missing -> 40.00.
	if p.Summary.GroupCost != 5000 {
		t.Errorf("GroupCost = %d, want 5000", p.Summary.GroupCost)
	}
	if p.Summary.Priced != 2 || p.Summary.Free != 1 || p.Summary.Unpriced != 1 || p.Summary.OtherMoney != 1 {
		t.Errorf("breakdown = priced %d free %d unpriced %d other %d",
			p.Summary.Priced, p.Summary.Free, p.Summary.Unpriced, p.Summary.OtherMoney)
	}
	if p.Summary.Currency != "USD" {
		t.Errorf("currency = %q", p.Summary.Currency)
	}
	// Gamma is free, so the crew can play it right now.
	if p.Summary.Ready != 1 {
		t.Errorf("Ready = %d, want 1", p.Summary.Ready)
	}
	if p.Summary.MissingForMe != 0 {
		t.Errorf("MissingForMe = %d, want 0", p.Summary.MissingForMe)
	}
	if p.Summary.MemberCount != 3 {
		t.Errorf("MemberCount = %d, want 3", p.Summary.MemberCount)
	}

	// For Bo the personal bill is Beta only.
	bo := members[1]
	if bo.DisplayName != "Bo" {
		t.Fatalf("second member = %q", bo.DisplayName)
	}
	pb, err := Build(context.Background(), st, cat, crew, bo.User, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if pb.Summary.MyCost != 2000 || pb.Summary.MissingForMe != 1 {
		t.Errorf("Bo MyCost = %d missing %d, want 2000/1", pb.Summary.MyCost, pb.Summary.MissingForMe)
	}
}

func TestBuildEntriesAndFilters(t *testing.T) {
	st, cat, crew, ada, members := fixture(t)
	p, err := Build(context.Background(), st, cat, crew, ada, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) != 5 {
		t.Fatalf("entries = %d, want 5", len(p.Entries))
	}
	if p.DataAge == 0 {
		t.Error("DataAge should be set once metadata is cached")
	}

	alpha := p.Entries[0]
	var found bool
	for _, e := range p.Entries {
		if e.AppID == 10 {
			alpha, found = e, true
		}
	}
	if !found {
		t.Fatal("Alpha missing")
	}
	if alpha.ReviewPercent() != 90 || alpha.Reviews.Description != "Very Positive" {
		t.Errorf("reviews = %+v (%d%%)", alpha.Reviews, alpha.ReviewPercent())
	}
	if alpha.Game.Metacritic.Score != 90 || alpha.Game.Developers[0] != "Studio" {
		t.Error("metadata not mapped")
	}
	if len(alpha.Owners) != 2 || alpha.Missing != 1 {
		t.Errorf("owners = %d, missing = %d", len(alpha.Owners), alpha.Missing)
	}

	// Bo sees one game they are missing; Ada sees none.
	pb, _ := Build(context.Background(), st, cat, crew, members[1].User, Options{Filter: FilterMissing})
	if len(pb.Entries) != 1 || pb.Entries[0].AppID != 20 {
		t.Fatalf("missing filter = %+v", pb.Entries)
	}
	pa, _ := Build(context.Background(), st, cat, crew, ada, Options{Filter: FilterMissing})
	if len(pa.Entries) != 0 {
		t.Fatalf("Ada should be missing nothing, got %d", len(pa.Entries))
	}

	// "playable now" is the free game plus anything everybody owns.
	pr, _ := Build(context.Background(), st, cat, crew, ada, Options{Filter: FilterReady})
	if len(pr.Entries) != 1 || pr.Entries[0].AppID != 30 {
		t.Fatalf("ready filter = %v", pr.Entries)
	}

	// Sorting by price puts Beta first, by name Alpha first.
	pp, _ := Build(context.Background(), st, cat, crew, ada, Options{Sort: SortPrice})
	if pp.Entries[0].AppID != 20 {
		t.Errorf("price sort first = %d, want 20", pp.Entries[0].AppID)
	}
	pn, _ := Build(context.Background(), st, cat, crew, ada, Options{Sort: SortName})
	if pn.Entries[0].Game.Name != "Alpha" {
		t.Errorf("name sort first = %q", pn.Entries[0].Game.Name)
	}
}

func TestSummaryUsesVotes(t *testing.T) {
	st, cat, crew, ada, _ := fixture(t)
	entries, _ := st.Entries(crew.ID, ada.ID)
	var alpha int64
	for _, e := range entries {
		if e.AppID == 10 {
			alpha = e.ID
		}
	}
	if err := st.Vote(crew.ID, alpha, ada.ID, 1); err != nil {
		t.Fatal(err)
	}
	p, err := Build(context.Background(), st, cat, crew, ada, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Summary.Upvotes != 1 {
		t.Errorf("Upvotes = %d, want 1", p.Summary.Upvotes)
	}
	// The default sort floats the up-voted game to the top.
	if p.Entries[0].AppID != 10 {
		t.Errorf("first entry = %d, want the up-voted Alpha", p.Entries[0].AppID)
	}
	counts := p.Counts()
	if counts[FilterLiked] != 1 {
		t.Errorf("liked count = %d", counts[FilterLiked])
	}
}

func TestHardNoDisqualifies(t *testing.T) {
	st, cat, crew, ada, members := fixture(t)
	entries, _ := st.Entries(crew.ID, ada.ID)
	var beta int64
	for _, e := range entries {
		if e.AppID == 20 {
			beta = e.ID
		}
	}
	if beta == 0 {
		t.Fatal("no Beta entry in the fixture")
	}
	// Bo says hard no to Beta, the 20.00 game two members are missing.
	bo := members[1]
	if err := st.Vote(crew.ID, beta, bo.ID, -1); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	p, err := Build(ctx, st, cat, crew, bo.User, Options{})
	if err != nil {
		t.Fatal(err)
	}

	// A vetoed game counts for nothing: it leaves the totals, the personal
	// bill and the readiness count, and is itself reported as disqualified.
	if p.Summary.Vetoed != 1 {
		t.Errorf("Vetoed = %d, want 1", p.Summary.Vetoed)
	}
	if p.Summary.Total != 1000 {
		t.Errorf("Total = %d, want 1000 (Alpha only)", p.Summary.Total)
	}
	if p.Summary.GroupCost != 1000 {
		t.Errorf("GroupCost = %d, want 1000 (Alpha, one member missing)", p.Summary.GroupCost)
	}
	if p.Summary.MyCost != 0 || p.Summary.MissingForMe != 0 {
		t.Errorf("MyCost = %d, MissingForMe = %d, want 0 and 0", p.Summary.MyCost, p.Summary.MissingForMe)
	}

	// Whatever the sort, the disqualified game is the last row and only once.
	for _, order := range []string{SortScore, SortAdded, SortPrice, SortName, SortMeta} {
		q, err := Build(ctx, st, cat, crew, bo.User, Options{Sort: order})
		if err != nil {
			t.Fatalf("%s: %v", order, err)
		}
		last := q.Entries[len(q.Entries)-1]
		if last.AppID != 20 {
			t.Errorf("%s sort: last row = %d, want the vetoed Beta", order, last.AppID)
		}
		for i, e := range q.Entries {
			if e.AppID == 20 && i != len(q.Entries)-1 {
				t.Errorf("%s sort: Beta at %d of %d, want last", order, i, len(q.Entries))
			}
		}
	}

	// The plain list still shows it (somebody has to be able to un-vote it),
	// but the "to buy" filter treats it as settled.
	active, err := Build(ctx, st, cat, crew, bo.User, Options{Filter: FilterActive})
	if err != nil {
		t.Fatal(err)
	}
	if !hasAppID(active.Entries, 20) {
		t.Error("the active list dropped the vetoed game instead of sinking it")
	}
	missing, err := Build(ctx, st, cat, crew, bo.User, Options{Filter: FilterMissing})
	if err != nil {
		t.Fatal(err)
	}
	if hasAppID(missing.Entries, 20) {
		t.Error("a vetoed game still shows up under \"to buy\"")
	}
	if n := active.Counts()[FilterMissing]; n != 0 {
		t.Errorf("missing count = %d, want 0", n)
	}

	// Taking the hard no back puts the game back in play.
	if err := st.Vote(crew.ID, beta, bo.ID, 0); err != nil {
		t.Fatal(err)
	}
	again, err := Build(ctx, st, cat, crew, bo.User, Options{Sort: SortPrice})
	if err != nil {
		t.Fatal(err)
	}
	if again.Summary.Vetoed != 0 || again.Summary.Total != 3000 {
		t.Errorf("after unveto: Vetoed = %d, Total = %d", again.Summary.Vetoed, again.Summary.Total)
	}
	if again.Entries[0].AppID != 20 {
		t.Errorf("after unveto the price sort first = %d, want 20", again.Entries[0].AppID)
	}
}

func hasAppID(entries []Entry, appid int) bool {
	for _, e := range entries {
		if e.AppID == appid {
			return true
		}
	}
	return false
}
