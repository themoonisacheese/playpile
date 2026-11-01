package store

import (
	"errors"
	"fmt"
	"playpile/internal/schedule"
	"testing"
	"time"
)

func scheduleFixture(t *testing.T) (*Store, Crew, User, User, User) {
	t.Helper()
	s := newStore(t)
	owner := newUser(t, s, "sched-owner", "Owner")
	member := newUser(t, s, "sched-member", "Member")
	outsider := newUser(t, s, "sched-outside", "Outsider")
	crew, err := s.CreateCrew("Schedule", "US", "english", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := s.CreateInvite(crew.ID, owner.ID, RoleMember, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RedeemInvite(inv.Token, member.ID); err != nil {
		t.Fatal(err)
	}
	return s, crew, owner, member, outsider
}
func futureDay() string { return time.Now().UTC().AddDate(0, 0, 2).Format("2006-01-02") }
func futureWindow(t *testing.T, date string) schedule.Interval {
	t.Helper()
	w, err := schedule.LocalWindow(date, "UTC", "22:00", "02:00")
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func readDays(t *testing.T, s *Store, c, u int64) []AvailabilityDay {
	t.Helper()
	days, err := s.AvailabilityDays(c, u, time.Now().UTC(), time.Now().UTC().Add(14*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return days
}
func TestSchedulingMigrationReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	owner := newUser(t, s, "old-user", "Old")
	crew, err := s.CreateCrew("Before", "US", "english", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Drop only additive scheduling tables, simulating the pre-feature schema.
	if _, err := s.db.Exec("DROP TABLE proposal_responses; DROP TABLE schedule_proposals; DROP TABLE availability_days; DROP TABLE planning_members;"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	date := futureDay()
	if err := s.SaveAvailabilityDay(crew.ID, owner.ID, date, "UTC", []schedule.Interval{futureWindow(t, date)}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if c, err := s.CrewByID(crew.ID); err != nil || c.Name != "Before" {
		t.Fatalf("old crew lost: %+v %v", c, err)
	}
	if days := readDays(t, s, crew.ID, owner.ID); len(days) != 1 || len(days[0].Windows) != 1 {
		t.Fatalf("days lost %+v", days)
	}
}
func TestAvailabilityAtomicityScopeZoneAndOvernight(t *testing.T) {
	s, c, o, m, x := scheduleFixture(t)
	date := futureDay()
	w := futureWindow(t, date)
	if err := s.SaveAvailabilityDay(c.ID, m.ID, date, "UTC", []schedule.Interval{w}); err != nil {
		t.Fatal(err)
	}
	invalid := []struct {
		date, zone string
		windows    []schedule.Interval
	}{
		{date, "Mars/Olympus", nil}, {"garbage", "UTC", nil}, {"2000-01-01", "UTC", nil},
		{time.Now().AddDate(0, 0, 29).Format("2006-01-02"), "UTC", nil},
		{date, "UTC", []schedule.Interval{{Start: w.End, End: w.Start}}},
		{date, "UTC", make([]schedule.Interval, 9)},
		{date, "UTC", []schedule.Interval{{Start: w.Start.Add(-48 * time.Hour), End: w.End}}},
	}
	for i, candidate := range invalid {
		if err := s.SaveAvailabilityDay(c.ID, m.ID, candidate.date, candidate.zone, candidate.windows); !errors.Is(err, ErrInvalid) {
			t.Fatalf("case%d: %v", i, err)
		}
		if days := readDays(t, s, c.ID, o.ID); len(days) != 1 || len(days[0].Windows) != 1 || !days[0].Windows[0].End.Equal(w.End) {
			t.Fatalf("invalid save lost data %+v", days)
		}
	}
	if err := s.SaveAvailabilityDay(c.ID, x.ID, date, "UTC", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outsider wrote: %v", err)
	}
	if _, err := s.AvailabilityDays(c.ID, x.ID, w.Start, w.End); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outsider read: %v", err)
	}
	if _, err := s.PlanningMembers(c.ID, x.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outsider profiles: %v", err)
	}
	if err := s.ClearAvailabilityDay(c.ID, x.ID, date); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outsider clear: %v", err)
	}
	if err := s.SaveAvailabilityDay(c.ID, m.ID, date, "Europe/Paris", nil); !errors.Is(err, ErrExists) {
		t.Fatalf("zone change allowed: %v", err)
	}
	next, _ := time.Parse("2006-01-02", date)
	nextDate := next.AddDate(0, 0, 1).Format("2006-01-02")
	if err := s.SaveAvailabilityDay(c.ID, m.ID, nextDate, "UTC", nil); err != nil {
		t.Fatal(err)
	}
	bounds, _ := schedule.DayBounds(nextDate, "UTC")
	days, err := s.AvailabilityDays(c.ID, o.ID, bounds.Start, bounds.End)
	if err != nil || len(days) != 2 || len(days[0].Windows) != 1 || len(days[1].Windows) != 0 {
		t.Fatalf("overnight lost %+v %v", days, err)
	}
	if err := s.ClearAvailabilityDay(c.ID, m.ID, date); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearAvailabilityDay(c.ID, m.ID, nextDate); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAvailabilityDay(c.ID, m.ID, date, "Europe/Paris", nil); err != nil {
		t.Fatalf("zone unlocked: %v", err)
	}
	pm, err := s.PlanningMembers(c.ID, o.ID)
	if err != nil || len(pm) != 2 {
		t.Fatalf("members %+v %v", pm, err)
	}
}
func TestProposalLifecycleScopeAndMembershipCascades(t *testing.T) {
	s, c, o, m, x := scheduleFixture(t)
	date := futureDay()
	w := futureWindow(t, date)
	if err := s.SaveAvailabilityDay(c.ID, m.ID, date, "UTC", []schedule.Interval{w}); err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProposal(c.ID, o.ID, "Game night", w.Start, w.End)
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.Proposals(c.ID, m.ID, w.Start, w.End)
	if err != nil || len(list) != 1 || len(list[0].Responses) != 0 {
		t.Fatalf("implicit RSVP %+v %v", list, err)
	}
	for _, response := range []string{"yes", "maybe", "no", ""} {
		if err := s.SetProposalRSVP(c.ID, m.ID, p.ID, response); err != nil {
			t.Fatal(err)
		}
		list, _ = s.Proposals(c.ID, o.ID, w.Start, w.End)
		if list[0].Responses[m.ID] != response {
			t.Fatalf("response %+v", list)
		}
	}
	if err := s.SetProposalRSVP(c.ID, m.ID, p.ID, "yes"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProposalRSVP(c.ID, x.ID, p.ID, "yes"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outsider RSVP %v", err)
	}
	if _, err := s.CreateProposal(c.ID, x.ID, "x", w.Start, w.End); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outsider create %v", err)
	}
	if err := s.CancelProposal(c.ID, m.ID, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("member cancelled other %v", err)
	}
	other, err := s.CreateCrew("Other", "US", "english", o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetProposalRSVP(other.ID, o.ID, p.ID, "yes"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross crew RSVP %v", err)
	}
	if err := s.CancelProposal(other.ID, o.ID, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross crew cancel %v", err)
	}
	if err := s.SaveAvailabilityDay(c.ID, m.ID, date, "UTC", nil); err != nil {
		t.Fatal(err)
	}
	list, _ = s.Proposals(c.ID, o.ID, w.Start, w.End)
	if list[0].Responses[m.ID] != "yes" {
		t.Fatal("availability rewrote RSVP")
	}
	if err := s.RemoveMember(c.ID, m.ID); err != nil {
		t.Fatal(err)
	}
	if len(readDays(t, s, c.ID, o.ID)) != 0 {
		t.Fatal("removed availability remains")
	}
	list, _ = s.Proposals(c.ID, o.ID, w.Start, w.End)
	if len(list[0].Responses) != 0 {
		t.Fatal("removed RSVP remains")
	}
	inv, _ := s.CreateInvite(c.ID, o.ID, RoleMember, 0, 0)
	if _, err := s.RedeemInvite(inv.Token, m.ID); err != nil {
		t.Fatal(err)
	}
	list, _ = s.Proposals(c.ID, m.ID, w.Start, w.End)
	if len(list[0].Responses) != 0 {
		t.Fatal("rejoined RSVP resurrected")
	}
	pm, _ := s.PlanningMembers(c.ID, m.ID)
	for _, member := range pm {
		if member.UserID == m.ID && member.Zone != "" {
			t.Fatal("rejoined timezone resurrected")
		}
	}
	p2, err := s.CreateProposal(c.ID, m.ID, "member proposal", w.Start, w.End)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CancelProposal(c.ID, o.ID, p2.ID); err != nil {
		t.Fatalf("admin cannot cancel: %v", err)
	}
	if err := s.CancelProposal(c.ID, o.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProposalRSVP(c.ID, m.ID, p.ID, "yes"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancelled RSVP %v", err)
	}
	list, _ = s.Proposals(c.ID, o.ID, w.Start, w.End)
	if !list[0].Cancelled {
		t.Fatal("not cancelled")
	}
}
func TestProposalLimitsAndPruning(t *testing.T) {
	s, c, o, _, _ := scheduleFixture(t)
	date := futureDay()
	w := futureWindow(t, date)
	for _, v := range []struct {
		title string
		a, b  time.Time
	}{
		{"bad", w.End, w.Start}, {"bad", time.Now().Add(-time.Hour), w.End}, {"bad", w.Start, w.Start.Add(13 * time.Hour)},
		{"bad", w.Start.Add(29 * 24 * time.Hour), w.End.Add(29 * 24 * time.Hour)}, {"bad\ncontrol", w.Start, w.End},
	} {
		if _, err := s.CreateProposal(c.ID, o.ID, v.title, v.a, v.b); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted invalid %+v %v", v, err)
		}
	}
	for i := 0; i < 64; i++ {
		p, err := s.CreateProposal(c.ID, o.ID, fmt.Sprint(i), w.Start, w.End)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CancelProposal(c.ID, o.ID, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateProposal(c.ID, o.ID, "overflow", w.Start, w.End); !errors.Is(err, ErrExists) {
		t.Fatalf("cap %v", err)
	}
	if _, err := s.db.Exec("UPDATE schedule_proposals SET end_at=?", now()-1); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAvailabilityDay(c.ID, o.ID, date, "UTC", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE availability_days SET expires_at=?", now()-1); err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProposal(c.ID, o.ID, "fresh", w.Start, w.End)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID <= 64 {
		t.Fatal("proposal identity reused")
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM availability_days").Scan(&n); err != nil || n != 0 {
		t.Fatalf("days not pruned %d %v", n, err)
	}
	list, err := s.Proposals(c.ID, o.ID, w.Start, w.End)
	if err != nil || len(list) != 1 {
		t.Fatalf("proposal prune %+v %v", list, err)
	}
	if _, err := s.Proposals(c.ID, o.ID, w.Start, w.Start.Add(29*24*time.Hour)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unbounded read %v", err)
	}
}
