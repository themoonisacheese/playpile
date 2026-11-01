package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"playpile/internal/schedule"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const schedulingSchema = `
CREATE TABLE IF NOT EXISTS planning_members (
 crew_id INTEGER NOT NULL, user_id INTEGER NOT NULL, zone TEXT NOT NULL,
 PRIMARY KEY(crew_id,user_id), FOREIGN KEY(crew_id,user_id) REFERENCES memberships(crew_id,user_id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS availability_days (
 crew_id INTEGER NOT NULL, user_id INTEGER NOT NULL, date TEXT NOT NULL, zone TEXT NOT NULL,
 start_at INTEGER NOT NULL, end_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, windows TEXT NOT NULL,
 PRIMARY KEY(crew_id,user_id,date), FOREIGN KEY(crew_id,user_id) REFERENCES memberships(crew_id,user_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS availability_expiry ON availability_days(expires_at);
CREATE INDEX IF NOT EXISTS availability_crew ON availability_days(crew_id,start_at);
CREATE TABLE IF NOT EXISTS schedule_proposals (
 id INTEGER PRIMARY KEY AUTOINCREMENT, crew_id INTEGER NOT NULL, created_by INTEGER NOT NULL,
 title TEXT NOT NULL, start_at INTEGER NOT NULL, end_at INTEGER NOT NULL, cancelled INTEGER NOT NULL DEFAULT 0,
 UNIQUE(id,crew_id), FOREIGN KEY(crew_id,created_by) REFERENCES memberships(crew_id,user_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS proposals_crew ON schedule_proposals(crew_id,start_at);
CREATE INDEX IF NOT EXISTS proposals_expiry ON schedule_proposals(end_at);
CREATE TABLE IF NOT EXISTS proposal_responses (
 proposal_id INTEGER NOT NULL, crew_id INTEGER NOT NULL, user_id INTEGER NOT NULL,
 response TEXT NOT NULL CHECK(response IN ('yes','maybe','no')),
 PRIMARY KEY(proposal_id,user_id),
 FOREIGN KEY(proposal_id,crew_id) REFERENCES schedule_proposals(id,crew_id) ON DELETE CASCADE,
 FOREIGN KEY(crew_id,user_id) REFERENCES memberships(crew_id,user_id) ON DELETE CASCADE
);`

func (s *Store) migrateScheduling() error { _, err := s.db.Exec(schedulingSchema); return err }

type AvailabilityDay struct {
	UserID     int64
	Date, Zone string
	Windows    []schedule.Interval
}
type PlanningMember struct {
	UserID int64
	Zone   string
}
type Proposal struct {
	ID, CrewID, CreatedBy int64
	Title                 string
	Start, End            time.Time
	Cancelled             bool
	Responses             map[int64]string
}

func schedulingRole(tx *sql.Tx, crewID, actorID int64) (string, error) {
	var role string
	err := tx.QueryRow("SELECT role FROM memberships WHERE crew_id=? AND user_id=?", crewID, actorID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return role, err
}
func pruneScheduling(tx *sql.Tx) error {
	if _, err := tx.Exec("DELETE FROM availability_days WHERE expires_at<=?", now()); err != nil {
		return err
	}
	_, err := tx.Exec("DELETE FROM schedule_proposals WHERE end_at<=?", now())
	return err
}
func scheduleRange(from, to time.Time) bool { return to.After(from) && to.Sub(from) <= 28*24*time.Hour }

func (s *Store) PlanningMembers(crewID, actorID int64) ([]PlanningMember, error) {
	if _, err := s.Membership(crewID, actorID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT m.user_id,COALESCE(p.zone,'') FROM memberships m LEFT JOIN planning_members p ON p.crew_id=m.crew_id AND p.user_id=m.user_id WHERE m.crew_id=? ORDER BY m.user_id`, crewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlanningMember
	for rows.Next() {
		var m PlanningMember
		if err := rows.Scan(&m.UserID, &m.Zone); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SaveAvailabilityDay atomically replaces the caller's reviewed day. Empty
// windows is an explicit unavailable answer; ClearAvailabilityDay means unknown.
func (s *Store) SaveAvailabilityDay(crewID, actorID int64, date, zone string, windows []schedule.Interval) error {
	bounds, err := schedule.DayBounds(date, zone)
	if err != nil || len(windows) > 8 {
		return ErrInvalid
	}
	loc, _ := time.LoadLocation(zone)
	today := time.Now().In(loc).Format("2006-01-02")
	horizon := time.Now().In(loc).AddDate(0, 0, 27).Format("2006-01-02")
	if date < today || date > horizon {
		return ErrInvalid
	}
	expiry := bounds.End.Unix()
	for _, w := range windows {
		if !w.End.After(w.Start) || w.End.Sub(w.Start) > 24*time.Hour || w.Start.Before(bounds.Start) || !w.Start.Before(bounds.End) || w.Start.Nanosecond() != 0 || w.End.Nanosecond() != 0 {
			return ErrInvalid
		}
		if w.End.Unix() > expiry {
			expiry = w.End.Unix()
		}
	}
	encoded, err := json.Marshal(windows)
	if err != nil {
		return ErrInvalid
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := schedulingRole(tx, crewID, actorID); err != nil {
		return err
	}
	if err := pruneScheduling(tx); err != nil {
		return err
	}
	var old string
	err = tx.QueryRow("SELECT zone FROM planning_members WHERE crew_id=? AND user_id=?", crewID, actorID).Scan(&old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if old != "" && old != zone {
		var count int
		if err := tx.QueryRow("SELECT COUNT(*) FROM availability_days WHERE crew_id=? AND user_id=? AND expires_at>?", crewID, actorID, now()).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return ErrExists
		}
	}
	if _, err := tx.Exec(`INSERT INTO planning_members(crew_id,user_id,zone) VALUES(?,?,?) ON CONFLICT(crew_id,user_id) DO UPDATE SET zone=excluded.zone`, crewID, actorID, zone); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO availability_days(crew_id,user_id,date,zone,start_at,end_at,expires_at,windows) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(crew_id,user_id,date) DO UPDATE SET zone=excluded.zone,start_at=excluded.start_at,end_at=excluded.end_at,expires_at=excluded.expires_at,windows=excluded.windows`, crewID, actorID, date, zone, bounds.Start.Unix(), bounds.End.Unix(), expiry, string(encoded)); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ClearAvailabilityDay(crewID, actorID int64, date string) error {
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return ErrInvalid
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := schedulingRole(tx, crewID, actorID); err != nil {
		return err
	}
	if err := pruneScheduling(tx); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM availability_days WHERE crew_id=? AND user_id=? AND date=?", crewID, actorID, date); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) AvailabilityDays(crewID, actorID int64, from, to time.Time) ([]AvailabilityDay, error) {
	if !scheduleRange(from, to) {
		return nil, ErrInvalid
	}
	if _, err := s.Membership(crewID, actorID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT user_id,date,zone,windows FROM availability_days WHERE crew_id=? AND start_at<? AND expires_at>? AND expires_at>? ORDER BY date,user_id`, crewID, to.Unix(), from.Unix(), now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AvailabilityDay
	for rows.Next() {
		var d AvailabilityDay
		var raw string
		if err := rows.Scan(&d.UserID, &d.Date, &d.Zone, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &d.Windows); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) CreateProposal(crewID, actorID int64, title string, start, end time.Time) (Proposal, error) {
	title = strings.TrimSpace(title)
	if utf8.RuneCountInString(title) > 160 || strings.IndexFunc(title, unicode.IsControl) >= 0 || !end.After(start) || start.Unix() <= now() || end.Sub(start) > 12*time.Hour || end.After(time.Now().Add(28*24*time.Hour)) || start.Nanosecond() != 0 || end.Nanosecond() != 0 {
		return Proposal{}, ErrInvalid
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Proposal{}, err
	}
	defer tx.Rollback()
	if _, err := schedulingRole(tx, crewID, actorID); err != nil {
		return Proposal{}, err
	}
	if err := pruneScheduling(tx); err != nil {
		return Proposal{}, err
	}
	var n int
	if err := tx.QueryRow("SELECT COUNT(*) FROM schedule_proposals WHERE crew_id=?", crewID).Scan(&n); err != nil {
		return Proposal{}, err
	}
	if n >= 64 {
		return Proposal{}, ErrExists
	}
	result, err := tx.Exec("INSERT INTO schedule_proposals(crew_id,created_by,title,start_at,end_at) VALUES(?,?,?,?,?)", crewID, actorID, title, start.Unix(), end.Unix())
	if err != nil {
		return Proposal{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Proposal{}, err
	}
	if err := tx.Commit(); err != nil {
		return Proposal{}, err
	}
	return Proposal{ID: id, CrewID: crewID, CreatedBy: actorID, Title: title, Start: start.UTC(), End: end.UTC(), Responses: map[int64]string{}}, nil
}
func (s *Store) Proposals(crewID, actorID int64, from, to time.Time) ([]Proposal, error) {
	if !scheduleRange(from, to) {
		return nil, ErrInvalid
	}
	if _, err := s.Membership(crewID, actorID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id,created_by,title,start_at,end_at,cancelled FROM schedule_proposals WHERE crew_id=? AND start_at<? AND end_at>? AND end_at>? ORDER BY start_at,id LIMIT 64`, crewID, to.Unix(), from.Unix(), now())
	if err != nil {
		return nil, err
	}
	var out []Proposal
	for rows.Next() {
		var p Proposal
		var a, b int64
		p.CrewID = crewID
		p.Responses = map[int64]string{}
		if err := rows.Scan(&p.ID, &p.CreatedBy, &p.Title, &a, &b, &p.Cancelled); err != nil {
			rows.Close()
			return nil, err
		}
		p.Start = time.Unix(a, 0).UTC()
		p.End = time.Unix(b, 0).UTC()
		out = append(out, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		r, err := s.db.Query("SELECT user_id,response FROM proposal_responses WHERE proposal_id=? AND crew_id=?", out[i].ID, crewID)
		if err != nil {
			return nil, err
		}
		for r.Next() {
			var id int64
			var response string
			if err := r.Scan(&id, &response); err != nil {
				r.Close()
				return nil, err
			}
			out[i].Responses[id] = response
		}
		err = r.Err()
		r.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (s *Store) SetProposalRSVP(crewID, actorID, proposalID int64, response string) error {
	if response != "" && response != "yes" && response != "maybe" && response != "no" {
		return ErrInvalid
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := schedulingRole(tx, crewID, actorID); err != nil {
		return err
	}
	if err := pruneScheduling(tx); err != nil {
		return err
	}
	var id int64
	if err := tx.QueryRow("SELECT id FROM schedule_proposals WHERE id=? AND crew_id=? AND cancelled=0 AND start_at>?", proposalID, crewID, now()).Scan(&id); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if response == "" {
		_, err = tx.Exec("DELETE FROM proposal_responses WHERE proposal_id=? AND user_id=?", proposalID, actorID)
	} else {
		_, err = tx.Exec(`INSERT INTO proposal_responses(proposal_id,crew_id,user_id,response) VALUES(?,?,?,?) ON CONFLICT(proposal_id,user_id) DO UPDATE SET response=excluded.response`, proposalID, crewID, actorID, response)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) CancelProposal(crewID, actorID, proposalID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	role, err := schedulingRole(tx, crewID, actorID)
	if err != nil {
		return err
	}
	if err := pruneScheduling(tx); err != nil {
		return err
	}
	var creator int64
	if err := tx.QueryRow("SELECT created_by FROM schedule_proposals WHERE id=? AND crew_id=? AND start_at>?", proposalID, crewID, now()).Scan(&creator); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if creator != actorID && Rank(role) < Rank(RoleAdmin) {
		return ErrNotFound
	}
	if _, err := tx.Exec("UPDATE schedule_proposals SET cancelled=1 WHERE id=? AND crew_id=?", proposalID, crewID); err != nil {
		return err
	}
	return tx.Commit()
}
