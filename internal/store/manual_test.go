package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func i64(v int64) *int64 { return &v }
func iptr(v int) *int    { return &v }

func TestManualEntriesMultipleAndEdit(t *testing.T) {
	s := newStore(t)
	a := newUser(t, s, "a", "Ada")
	b := newUser(t, s, "b", "Bob")
	crew, _ := s.CreateCrew("Crew", "US", "english", a.ID)

	mc, err := s.AddManualEntry(crew.ID, ManualGame{
		Name:        "  Minecraft  ",
		Description: " blocks ",
		ImageURL:    "https://example.com/mc.png",
		StoreURL:    "https://www.minecraft.net",
		PriceMinor:  i64(2999),
		Currency:    "usd",
		MetaScore:   iptr(93),
	}, a.ID)
	if err != nil {
		t.Fatalf("add manual entry: %v", err)
	}
	if mc.ID == 0 || mc.AppID != 0 || mc.CrewID != crew.ID || mc.Status != StatusActive || mc.AddedBy != a.ID {
		t.Fatalf("returned entry = %+v", mc)
	}
	if mc.Manual == nil {
		t.Fatal("returned entry has no manual payload")
	}
	if mc.Manual.Name != "Minecraft" || mc.Manual.Description != "blocks" || mc.Manual.Currency != "USD" {
		t.Fatalf("normalised payload = %+v", mc.Manual)
	}
	if mc.Manual.PriceMinor == nil || *mc.Manual.PriceMinor != 2999 || mc.Manual.MetaScore == nil || *mc.Manual.MetaScore != 93 {
		t.Fatalf("numbers = %+v", mc.Manual)
	}

	// The same manual game may be listed twice: SQLite treats NULL appids as
	// distinct, so there is no fake Steam id needed to make duplicates work.
	second, err := s.AddManualEntry(crew.ID, ManualGame{Name: "Minecraft (Java)"}, b.ID)
	if err != nil {
		t.Fatalf("second manual entry: %v", err)
	}
	if second.ID == mc.ID {
		t.Fatal("two manual entries shared an id")
	}

	// A Steam entry in the same crew stays a Steam entry.
	steam, err := s.AddEntry(crew.ID, 440, a.ID)
	if err != nil {
		t.Fatalf("add steam entry: %v", err)
	}

	entries, err := s.Entries(crew.ID, a.ID)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	if entries[0].Manual == nil || entries[1].Manual == nil {
		t.Fatalf("manual payload missing from the list: %+v", entries)
	}
	if entries[2].Manual != nil || entries[2].AppID != 440 {
		t.Fatalf("steam entry = %+v", entries[2])
	}
	if got, err := s.EntryByID(crew.ID, mc.ID); err != nil || got.Manual == nil || got.Manual.Name != "Minecraft" {
		t.Fatalf("EntryByID manual = %+v, %v", got, err)
	}
	if got, err := s.EntryByID(crew.ID, steam.ID); err != nil || got.Manual != nil {
		t.Fatalf("EntryByID steam = %+v, %v", got, err)
	}

	// Editing one manual entry changes only that one.
	if err := s.UpdateManualEntry(crew.ID, mc.ID, ManualGame{
		Name:        "Minecraft: Java Edition",
		Description: "survival, mostly",
		StoreURL:    "http://example.com/buy",
		PriceMinor:  i64(0),
		Currency:    "USD",
	}); err != nil {
		t.Fatalf("update manual entry: %v", err)
	}
	updated, err := s.EntryByID(crew.ID, mc.ID)
	if err != nil {
		t.Fatalf("reload updated: %v", err)
	}
	if updated.Manual.Name != "Minecraft: Java Edition" || updated.Manual.ImageURL != "" {
		t.Fatalf("updated payload = %+v", updated.Manual)
	}
	if updated.Manual.PriceMinor == nil || *updated.Manual.PriceMinor != 0 {
		t.Fatalf("free price must survive as 0, got %+v", updated.Manual)
	}
	if updated.Manual.MetaScore != nil {
		t.Fatalf("metascore should have been cleared, got %v", *updated.Manual.MetaScore)
	}
	sibling, _ := s.EntryByID(crew.ID, second.ID)
	if sibling.Manual.Name != "Minecraft (Java)" {
		t.Fatalf("editing one entry changed another: %+v", sibling.Manual)
	}

	// Isolation: another crew can neither see nor edit it.
	other, _ := s.CreateCrew("Other", "US", "english", b.ID)
	if _, err := s.EntryByID(other.ID, mc.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-crew EntryByID err = %v, want ErrNotFound", err)
	}
	if err := s.UpdateManualEntry(other.ID, mc.ID, ManualGame{Name: "Hijacked"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-crew update err = %v, want ErrNotFound", err)
	}
	if got, _ := s.EntryByID(crew.ID, mc.ID); got.Manual.Name != "Minecraft: Java Edition" {
		t.Fatalf("cross-crew update changed the entry: %+v", got.Manual)
	}
	// Steam entries are not editable through the manual API.
	if err := s.UpdateManualEntry(crew.ID, steam.ID, ManualGame{Name: "No"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("steam update err = %v, want ErrInvalid", err)
	}
}

func TestManualEntryValidation(t *testing.T) {
	s := newStore(t)
	a := newUser(t, s, "a", "Ada")
	crew, _ := s.CreateCrew("Crew", "US", "english", a.ID)

	good := ManualGame{Name: "Ok", PriceMinor: i64(1000), Currency: "USD", MetaScore: iptr(50)}
	longName := strings.Repeat("x", 201)
	longDesc := strings.Repeat("d", 2001)
	longURL := "https://example.com/" + strings.Repeat("p", 2048)

	bad := []struct {
		name string
		game ManualGame
	}{
		{"empty name", ManualGame{Name: "   "}},
		{"name too long", ManualGame{Name: longName}},
		{"description too long", ManualGame{Name: "Ok", Description: longDesc}},
		{"ftp url", ManualGame{Name: "Ok", StoreURL: "ftp://example.com/game"}},
		{"javascript url", ManualGame{Name: "Ok", ImageURL: "javascript:alert(1)"}},
		{"relative url", ManualGame{Name: "Ok", StoreURL: "/game/1"}},
		{"scheme-relative url", ManualGame{Name: "Ok", StoreURL: "//example.com/game"}},
		{"hostless url", ManualGame{Name: "Ok", StoreURL: "https://"}},
		{"credentials in url", ManualGame{Name: "Ok", StoreURL: "https://user:secret@example.com/game"}},
		{"url too long", ManualGame{Name: "Ok", ImageURL: longURL}},
		{"negative price", ManualGame{Name: "Ok", PriceMinor: i64(-1), Currency: "USD"}},
		{"price over the cap", ManualGame{Name: "Ok", PriceMinor: i64(1_000_000_000_001), Currency: "USD"}},
		{"priced without currency", ManualGame{Name: "Ok", PriceMinor: i64(1000)}},
		{"priced with short currency", ManualGame{Name: "Ok", PriceMinor: i64(1000), Currency: "US"}},
		{"priced with digits in currency", ManualGame{Name: "Ok", PriceMinor: i64(1000), Currency: "US1"}},
		{"unpriced with bad currency", ManualGame{Name: "Ok", Currency: "EU"}},
		{"metascore below zero", ManualGame{Name: "Ok", MetaScore: iptr(-1)}},
		{"metascore above hundred", ManualGame{Name: "Ok", MetaScore: iptr(101)}},
	}
	for _, tc := range bad {
		if _, err := s.AddManualEntry(crew.ID, tc.game, a.ID); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: add err = %v, want ErrInvalid", tc.name, err)
		}
	}
	if entries, _ := s.Entries(crew.ID, a.ID); len(entries) != 0 {
		t.Fatalf("rejected entries were stored: %+v", entries)
	}

	// The exact limits are allowed.
	okGames := []ManualGame{
		{Name: strings.Repeat("n", 200)},
		{Name: "Ok", Description: strings.Repeat("d", 2000)},
		{Name: "Ok", PriceMinor: i64(1_000_000_000_000), Currency: "usd"},
		{Name: "Ok", PriceMinor: i64(0), Currency: "PLN"},
		{Name: "Ok", PriceMinor: i64(500), Currency: "EUR", MetaScore: iptr(0)},
		{Name: "Ok", MetaScore: iptr(100)},
		{Name: "Ok", ImageURL: "http://example.com/a.png", StoreURL: "https://example.com/b"},
		{Name: "Ok", StoreURL: "https://example.com/" + strings.Repeat("p", 2048-len("https://example.com/"))},
	}
	for i, g := range okGames {
		if _, err := s.AddManualEntry(crew.ID, g, a.ID); err != nil {
			t.Fatalf("valid game %d rejected: %v", i, err)
		}
	}

	// An invalid edit is atomic: the stored row is unchanged.
	entry, err := s.AddManualEntry(crew.ID, good, a.ID)
	if err != nil {
		t.Fatalf("add good: %v", err)
	}
	before, _ := s.EntryByID(crew.ID, entry.ID)
	for _, tc := range bad {
		if err := s.UpdateManualEntry(crew.ID, entry.ID, tc.game); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: update err = %v, want ErrInvalid", tc.name, err)
		}
	}
	after, err := s.EntryByID(crew.ID, entry.ID)
	if err != nil {
		t.Fatalf("reload after failed updates: %v", err)
	}
	if after.Manual.Name != before.Manual.Name || *after.Manual.PriceMinor != *before.Manual.PriceMinor ||
		after.Manual.Currency != before.Manual.Currency || *after.Manual.MetaScore != *before.Manual.MetaScore {
		t.Fatalf("a rejected update changed the row: before %+v, after %+v", before.Manual, after.Manual)
	}

	// Unknown crew is not found, not created.
	if _, err := s.AddManualEntry(9999, ManualGame{Name: "Ghost"}, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown crew err = %v, want ErrNotFound", err)
	}
}

func TestManualOwnershipAndVotes(t *testing.T) {
	s := newStore(t)
	a := newUser(t, s, "a", "Ada")
	b := newUser(t, s, "b", "Bob")
	c := newUser(t, s, "c", "Cleo")
	crew, _ := s.CreateCrew("Crew", "US", "english", a.ID)
	inv, _ := s.CreateInvite(crew.ID, a.ID, RoleMember, 0, 0)
	if _, err := s.RedeemInvite(inv.Token, b.ID); err != nil {
		t.Fatalf("invite bob: %v", err)
	}

	mc, err := s.AddManualEntry(crew.ID, ManualGame{Name: "Minecraft"}, a.ID)
	if err != nil {
		t.Fatalf("add manual: %v", err)
	}
	second, err := s.AddManualEntry(crew.ID, ManualGame{Name: "Terraria"}, a.ID)
	if err != nil {
		t.Fatalf("add second manual: %v", err)
	}
	steam, _ := s.AddEntry(crew.ID, 440, a.ID)

	// Ownership is per entry and per member.
	if err := s.SetManualOwned(crew.ID, mc.ID, c.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-member ownership err = %v, want ErrNotFound", err)
	}
	if err := s.SetManualOwned(crew.ID, mc.ID, a.ID, true); err != nil {
		t.Fatalf("own manual: %v", err)
	}
	if err := s.SetManualOwned(crew.ID, mc.ID, b.ID, true); err != nil {
		t.Fatalf("bob owns manual: %v", err)
	}
	if err := s.SetManualOwned(crew.ID, mc.ID, a.ID, true); err != nil {
		t.Fatalf("owning twice must be idempotent: %v", err)
	}
	owns, err := s.CrewManualOwnership(crew.ID)
	if err != nil {
		t.Fatalf("crew manual ownership: %v", err)
	}
	if !owns[mc.ID][a.ID] || !owns[mc.ID][b.ID] || len(owns[mc.ID]) != 2 || len(owns) != 1 {
		t.Fatalf("ownership = %+v", owns)
	}
	if err := s.SetManualOwned(crew.ID, mc.ID, a.ID, false); err != nil {
		t.Fatalf("unown: %v", err)
	}
	owns, _ = s.CrewManualOwnership(crew.ID)
	if owns[mc.ID][a.ID] || !owns[mc.ID][b.ID] || len(owns[mc.ID]) != 1 {
		t.Fatalf("ownership after untick = %+v", owns)
	}
	if owns[second.ID] != nil {
		t.Fatalf("ownership leaked to another entry: %+v", owns)
	}

	// Scoping: another crew, and a Steam entry, refuse the manual ownership API.
	other, _ := s.CreateCrew("Other", "US", "english", c.ID)
	if err := s.SetManualOwned(other.ID, mc.ID, c.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-crew ownership err = %v, want ErrNotFound", err)
	}
	if err := s.SetManualOwned(crew.ID, steam.ID, a.ID, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("steam ownership through manual API err = %v, want ErrInvalid", err)
	}

	// Votes work on manual entries exactly like on Steam ones, including the
	// hard no that the list turns into a veto.
	if err := s.Vote(crew.ID, mc.ID, a.ID, 1); err != nil {
		t.Fatalf("vote up: %v", err)
	}
	if err := s.Vote(crew.ID, mc.ID, b.ID, -1); err != nil {
		t.Fatalf("vote down: %v", err)
	}
	entries, _ := s.Entries(crew.ID, b.ID)
	var got Entry
	for _, e := range entries {
		if e.ID == mc.ID {
			got = e
		}
	}
	if got.Up != 1 || got.Down != 1 || got.MyVote != -1 || got.Manual == nil {
		t.Fatalf("manual entry tallies = %+v", got)
	}

	// A removed member stops counting immediately, although their ownership
	// mark is kept like Steam ownership, which belongs to the person.
	if err := s.RemoveMember(crew.ID, a.ID); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	owns, err = s.CrewManualOwnership(crew.ID)
	if err != nil {
		t.Fatalf("ownership after removal: %v", err)
	}
	if len(owns) != 1 || owns[mc.ID][a.ID] || !owns[mc.ID][b.ID] {
		t.Fatalf("removed member still counts as an owner: %+v", owns)
	}
	entries, _ = s.Entries(crew.ID, b.ID)
	for _, e := range entries {
		if e.ID == mc.ID && e.Up != 0 {
			t.Fatalf("removed member's vote survived: %+v", e)
		}
	}

	// Deleting the entry clears its votes and ownership marks.
	if err := s.DeleteEntry(crew.ID, mc.ID); err != nil {
		t.Fatalf("delete manual entry: %v", err)
	}
	if _, err := s.EntryByID(crew.ID, mc.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted entry still loads: %v", err)
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM votes WHERE entry_id = ?", mc.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("votes after delete = %d, %v", n, err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM manual_ownership WHERE entry_id = ?", mc.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("manual ownership after delete = %d, %v", n, err)
	}
	if owns, _ := s.CrewManualOwnership(crew.ID); len(owns) != 0 {
		t.Fatalf("ownership map after delete = %+v", owns)
	}
	if err := s.DeleteEntry(crew.ID, mc.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}
	// The surviving manual entry and its data are untouched.
	if left, err := s.EntryByID(crew.ID, second.ID); err != nil || left.Manual == nil || left.Manual.Name != "Terraria" {
		t.Fatalf("sibling entry after delete = %+v, %v", left, err)
	}
}

func TestManualEntrySurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	a, err := s.UpsertSteamUser("a", "Ada", "", "")
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	crew, _ := s.CreateCrew("Crew", "US", "english", a.ID)
	e, err := s.AddManualEntry(crew.ID, ManualGame{
		Name:       "Minecraft",
		StoreURL:   "https://www.minecraft.net",
		PriceMinor: i64(1999),
		Currency:   "USD",
		MetaScore:  iptr(93),
	}, a.ID)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := s.SetManualOwned(crew.ID, e.ID, a.ID, true); err != nil {
		t.Fatalf("own: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err = Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	got, err := s.EntryByID(crew.ID, e.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Manual == nil || got.Manual.Name != "Minecraft" || got.Manual.StoreURL != "https://www.minecraft.net" {
		t.Fatalf("manual payload after reopen = %+v", got.Manual)
	}
	if got.Manual.PriceMinor == nil || *got.Manual.PriceMinor != 1999 || got.Manual.Currency != "USD" || *got.Manual.MetaScore != 93 {
		t.Fatalf("manual numbers after reopen = %+v", got.Manual)
	}
	owns, err := s.CrewManualOwnership(crew.ID)
	if err != nil || !owns[e.ID][a.ID] {
		t.Fatalf("manual ownership after reopen = %+v, %v", owns, err)
	}
	var appid sql.NullInt64
	if err := s.db.QueryRow("SELECT appid FROM entries WHERE id = ?", e.ID).Scan(&appid); err != nil || appid.Valid {
		t.Fatalf("appid after reopen = %+v, %v (manual entries must keep NULL)", appid, err)
	}
}
