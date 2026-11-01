package store

import (
	"errors"
	"playpile/internal/schedule"
	"strings"
	"testing"
	"time"
)

func TestProposalUnicodeAndCreatorCancellation(t *testing.T) {
	s, c, o, m, _ := scheduleFixture(t)
	w := futureWindow(t, futureDay())
	p, err := s.CreateProposal(c.ID, m.ID, strings.Repeat("遊", 160), w.Start, w.End)
	if err != nil {
		t.Fatalf("160 runes rejected: %v", err)
	}
	if _, err := s.CreateProposal(c.ID, m.ID, strings.Repeat("遊", 161), w.Start, w.End); !errors.Is(err, ErrInvalid) {
		t.Fatalf("161 runes accepted: %v", err)
	}
	if err := s.CancelProposal(c.ID, m.ID, p.ID); err != nil {
		t.Fatalf("creator cancellation: %v", err)
	}
	if err := s.SetProposalRSVP(c.ID, o.ID, p.ID, "yes"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancelled response: %v", err)
	}
	p, err = s.CreateProposal(c.ID, m.ID, "removed creator", w.Start, w.End)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveMember(c.ID, m.ID); err != nil {
		t.Fatal(err)
	}
	list, err := s.Proposals(c.ID, o.ID, w.Start, w.End)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range list {
		if candidate.ID == p.ID {
			t.Fatal("removed creator proposal retained")
		}
	}
}
func TestSpillingPastDateRetainsZoneLock(t *testing.T) {
	s, c, o, m, _ := scheduleFixture(t)
	today := time.Now().UTC().Format("2006-01-02")
	// Simulate a yesterday review with an absolute overnight tail still future.
	start := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	end := start.Add(3 * time.Hour)
	if _, err := s.db.Exec("INSERT INTO planning_members(crew_id,user_id,zone) VALUES(?,?,?)", c.ID, m.ID, "UTC"); err != nil {
		t.Fatal(err)
	}
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	if _, err := s.db.Exec(`INSERT INTO availability_days(crew_id,user_id,date,zone,start_at,end_at,expires_at,windows) VALUES(?,?,?,?,?,?,?,?)`, c.ID, m.ID, yesterday, "UTC", start.Unix(), start.Unix(), end.Unix(), "[]"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAvailabilityDay(c.ID, m.ID, today, "Europe/Paris", []schedule.Interval{}); !errors.Is(err, ErrExists) && !errors.Is(err, ErrInvalid) {
		t.Fatalf("spilling day zone lock: %v", err)
	}
	date := futureDay()
	if err := s.SaveAvailabilityDay(c.ID, m.ID, date, "Europe/Paris", nil); !errors.Is(err, ErrExists) {
		t.Fatalf("spilling future zone lock: %v", err)
	}
	if err := s.ClearAvailabilityDay(c.ID, m.ID, yesterday); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAvailabilityDay(c.ID, m.ID, date, "Europe/Paris", nil); err != nil {
		t.Fatalf("deliberate clear unlock: %v", err)
	}
	if days := readDays(t, s, c.ID, o.ID); len(days) != 1 {
		t.Fatalf("clear deleted other data %+v", days)
	}
}
