package pile

import (
	"context"
	"playpile/internal/catalog"
	"playpile/internal/steam"
	"playpile/internal/store"
	"testing"
)

type noManualSteam struct{ t *testing.T }

func (f noManualSteam) AppDetails(context.Context, int, string, string) (steam.Game, error) {
	f.t.Error("manual game queried Steam metadata")
	return steam.Game{}, steam.ErrNotFound
}
func (f noManualSteam) Reviews(context.Context, int) (steam.Reviews, error) {
	f.t.Error("manual game queried Steam reviews")
	return steam.Reviews{}, steam.ErrNotFound
}
func (f noManualSteam) Search(context.Context, string, string, string) ([]steam.SearchResult, error) {
	f.t.Error("manual game searched Steam")
	return nil, nil
}

func TestManualGamesUseNormalTotalsAndVetoes(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ada, _ := st.UpsertSteamUser("a", "Ada", "", "")
	bo, _ := st.UpsertSteamUser("b", "Bo", "", "")
	crew, _ := st.CreateCrew("Manual", "US", "english", ada.ID)
	inv, _ := st.CreateInvite(crew.ID, ada.ID, store.RoleMember, 0, 0)
	if _, err := st.RedeemInvite(inv.Token, bo.ID); err != nil {
		t.Fatal(err)
	}
	price := int64(2500)
	game, err := st.AddManualEntry(crew.ID, store.ManualGame{Name: "Minecraft", PriceMinor: &price, Currency: "USD"}, ada.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.AddManualEntry(crew.ID, store.ManualGame{Name: "Another non-Steam game"}, ada.ID)
	if err != nil {
		t.Fatal(err)
	}
	cat := catalog.New(st, noManualSteam{t})
	build := func() *Pile {
		t.Helper()
		p, err := Build(context.Background(), st, cat, crew, ada, Options{})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := build()
	if p.Summary.Total != 2500 || p.Summary.GroupCost != 5000 || p.Summary.MyCost != 2500 || p.Summary.Unpriced != 1 {
		t.Fatalf("totals: %+v", p.Summary)
	}
	if err := st.SetManualOwned(crew.ID, game.ID, ada.ID, true); err != nil {
		t.Fatal(err)
	}
	p = build()
	if p.Summary.MyCost != 0 || p.Summary.GroupCost != 2500 {
		t.Fatalf("ownership totals: %+v", p.Summary)
	}
	for _, e := range p.All {
		if e.ID == other.ID && e.OwnedByMe {
			t.Fatal("ownership leaked to another manual game")
		}
	}
	if err := st.Vote(crew.ID, game.ID, bo.ID, -1); err != nil {
		t.Fatal(err)
	}
	p = build()
	if p.Summary.Total != 0 || p.Summary.GroupCost != 0 || p.Summary.Vetoed != 1 || p.Entries[len(p.Entries)-1].ID != game.ID {
		t.Fatalf("veto: %+v", p.Summary)
	}
	if err := st.Vote(crew.ID, game.ID, bo.ID, 0); err != nil {
		t.Fatal(err)
	}
	crew.Country = "FR"
	p = build()
	if p.Summary.Currency != "EUR" || p.Summary.Total != 0 || p.Summary.OtherMoney != 1 {
		t.Fatalf("region change silently converted prices: %+v", p.Summary)
	}
}
