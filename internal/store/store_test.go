package store

import (
	"errors"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newUser(t *testing.T, s *Store, steamID, name string) User {
	t.Helper()
	u, err := s.UpsertSteamUser(steamID, name, "", "")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

func TestCreateCrewMakesOwner(t *testing.T) {
	s := newStore(t)
	u := newUser(t, s, "76561197960287930", "Ada")

	crew, err := s.CreateCrew("Friday night", "pl", "english", u.ID)
	if err != nil {
		t.Fatalf("create crew: %v", err)
	}
	if crew.Country != "PL" {
		t.Fatalf("country = %q, want normalised PL", crew.Country)
	}
	mem, err := s.Membership(crew.ID, u.ID)
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	if mem.Role != RoleOwner {
		t.Fatalf("role = %q, want owner", mem.Role)
	}
	if !mem.Can(RoleAdmin) {
		t.Fatal("owner should pass admin checks")
	}

	crews, err := s.CrewsForUser(u.ID)
	if err != nil || len(crews) != 1 {
		t.Fatalf("crews = %v, err %v", crews, err)
	}
	if crews[0].Members != 1 || crews[0].Entries != 0 {
		t.Fatalf("counts = %+v", crews[0])
	}
}

func TestInviteLifecycle(t *testing.T) {
	s := newStore(t)
	owner := newUser(t, s, "111", "Owner")
	guest := newUser(t, s, "222", "Guest")
	crew, _ := s.CreateCrew("Crew", "US", "english", owner.ID)

	inv, err := s.CreateInvite(crew.ID, owner.ID, RoleMember, 0, 2)
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}

	got, err := s.InviteByToken(inv.Token)
	if err != nil || got.CrewID != crew.ID {
		t.Fatalf("invite lookup: %+v %v", got, err)
	}
	if !got.Usable(time.Now().Unix()) {
		t.Fatal("fresh invite should be usable")
	}

	crewID, err := s.RedeemInvite(inv.Token, guest.ID)
	if err != nil || crewID != crew.ID {
		t.Fatalf("redeem: %d %v", crewID, err)
	}
	mem, err := s.Membership(crew.ID, guest.ID)
	if err != nil || mem.Role != RoleMember {
		t.Fatalf("membership after redeem: %+v %v", mem, err)
	}
	got, _ = s.InviteByToken(inv.Token)
	if got.Uses != 1 {
		t.Fatalf("uses = %d, want 1", got.Uses)
	}

	// Redeeming again is a no-op, not an error, and does not burn a use.
	if _, err := s.RedeemInvite(inv.Token, guest.ID); err != nil {
		t.Fatalf("second redeem: %v", err)
	}
	got, _ = s.InviteByToken(inv.Token)
	if got.Uses != 1 {
		t.Fatalf("uses after repeat = %d, want 1", got.Uses)
	}

	// Exhaust it with a third person.
	third := newUser(t, s, "333", "Third")
	if _, err := s.RedeemInvite(inv.Token, third.ID); err != nil {
		t.Fatalf("third redeem: %v", err)
	}
	fourth := newUser(t, s, "444", "Fourth")
	if _, err := s.RedeemInvite(inv.Token, fourth.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("exhausted invite err = %v, want ErrNotFound", err)
	}

	// Revoked and expired invites refuse redemption.
	rev, _ := s.CreateInvite(crew.ID, owner.ID, RoleMember, 0, 0)
	if err := s.RevokeInvite(crew.ID, rev.Token); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := s.RedeemInvite(rev.Token, newUser(t, s, "555", "Fifth").ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked invite err = %v", err)
	}

	old, _ := s.CreateInvite(crew.ID, owner.ID, RoleMember, time.Now().Add(-time.Hour).Unix(), 0)
	if _, err := s.RedeemInvite(old.Token, newUser(t, s, "666", "Sixth").ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired invite err = %v", err)
	}

	if _, err := s.CreateInvite(crew.ID, owner.ID, RoleOwner, 0, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("owner invite err = %v, want ErrInvalid", err)
	}
}

func TestEntriesVotesOwnership(t *testing.T) {
	s := newStore(t)
	a := newUser(t, s, "a", "A")
	b := newUser(t, s, "b", "B")
	crew, _ := s.CreateCrew("Crew", "US", "english", a.ID)

	e1, err := s.AddEntry(crew.ID, 440, a.ID)
	if err != nil {
		t.Fatalf("add entry: %v", err)
	}
	if _, err := s.AddEntry(crew.ID, 440, a.ID); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate add err = %v, want ErrExists", err)
	}

	if err := s.Vote(crew.ID, e1.ID, a.ID, 1); err != nil {
		t.Fatalf("vote: %v", err)
	}
	if err := s.Vote(crew.ID, e1.ID, b.ID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("vote by non-member err = %v, want ErrNotFound", err)
	}

	entries, err := s.Entries(crew.ID, a.ID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries: %+v %v", entries, err)
	}
	if entries[0].Up != 1 || entries[0].MyVote != 1 {
		t.Fatalf("tallies = %+v", entries[0])
	}

	// Changing a vote replaces it rather than stacking.
	if err := s.Vote(crew.ID, e1.ID, a.ID, -1); err != nil {
		t.Fatalf("revote: %v", err)
	}
	entries, _ = s.Entries(crew.ID, a.ID)
	if entries[0].Up != 0 || entries[0].Down != 1 || entries[0].MyVote != -1 {
		t.Fatalf("tallies after revote = %+v", entries[0])
	}

	// A vote cannot reach into another crew's entry.
	other, _ := s.CreateCrew("Other", "US", "english", b.ID)
	if err := s.Vote(other.ID, e1.ID, b.ID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-crew vote err = %v, want ErrNotFound", err)
	}

	inv, _ := s.CreateInvite(crew.ID, a.ID, RoleMember, 0, 0)
	if _, err := s.RedeemInvite(inv.Token, b.ID); err != nil {
		t.Fatalf("invite b: %v", err)
	}
	if err := s.SetOwned(b.ID, 440, true, SourceManual); err != nil {
		t.Fatalf("set owned: %v", err)
	}
	if err := s.ReplaceLibrary(a.ID, []int{440, 570}); err != nil {
		t.Fatalf("library: %v", err)
	}
	owners, err := s.CrewOwnership(crew.ID)
	if err != nil {
		t.Fatalf("crew ownership: %v", err)
	}
	if !owners[440][a.ID] || !owners[440][b.ID] {
		t.Fatalf("owners = %+v", owners)
	}

	// Unticking removes only manual marks; the library import survives.
	if err := s.SetOwned(a.ID, 440, false, SourceManual); err != nil {
		t.Fatalf("unset owned: %v", err)
	}
	if owned, _ := s.OwnedApps(a.ID); !owned[440] {
		t.Fatal("library ownership should survive a manual untick")
	}
	if err := s.ReplaceLibrary(a.ID, []int{570}); err != nil {
		t.Fatalf("library replace: %v", err)
	}
	if owned, _ := s.OwnedApps(a.ID); owned[440] {
		t.Fatal("stale library entry should have been dropped")
	}

	// Removing a member removes their votes in that crew.
	if err := s.RemoveMember(crew.ID, a.ID); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	entries, _ = s.Entries(crew.ID, b.ID)
	if entries[0].Down != 0 {
		t.Fatalf("votes should be gone with the member: %+v", entries[0])
	}
}

func TestSessionsAndSoftDelete(t *testing.T) {
	s := newStore(t)
	u := newUser(t, s, "x", "X")

	sess, err := s.CreateSession(u.ID, time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	got, err := s.SessionByID(sess.ID)
	if err != nil || got.UserID != u.ID || got.CSRF != sess.CSRF {
		t.Fatalf("session: %+v %v", got, err)
	}
	_ = s.DeleteSession(sess.ID)
	if _, err := s.SessionByID(sess.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session should be gone, err = %v", err)
	}

	expired, _ := s.CreateSession(u.ID, -time.Hour)
	if _, err := s.SessionByID(expired.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session err = %v", err)
	}
}

func TestDuplicateSteamLoginUpdatesName(t *testing.T) {
	s := newStore(t)
	first, err := s.UpsertSteamUser("76561197960287930", "Old name", "", "")
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	second, err := s.UpsertSteamUser("76561197960287930", "New name", "https://avatar", "")
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	if first.ID != second.ID {
		t.Fatal("a repeat login must reuse the same user row")
	}
	if second.DisplayName != "New name" || second.AvatarURL != "https://avatar" {
		t.Fatalf("profile not refreshed: %+v", second)
	}
	// A login without profile data must not blank the avatar.
	third, _ := s.UpsertSteamUser("76561197960287930", "", "", "")
	if third.AvatarURL != "https://avatar" || third.DisplayName != "New name" {
		t.Fatalf("empty login wiped profile: %+v", third)
	}
}
