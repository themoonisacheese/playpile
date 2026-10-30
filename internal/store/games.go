package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// AddEntry puts a game on a crew's list. Returns ErrExists if it is already there.
func (s *Store) AddEntry(crewID int64, appid int, addedBy int64) (Entry, error) {
	if appid <= 0 {
		return Entry{}, ErrInvalid
	}
	ts := now()
	res, err := s.db.Exec(`
		INSERT INTO entries (crew_id, appid, added_by, added_at, status)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(crew_id, appid) DO NOTHING`,
		crewID, appid, addedBy, ts, StatusActive)
	if err != nil {
		return Entry{}, fmt.Errorf("add entry: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Entry{}, ErrExists
	}
	id, _ := res.LastInsertId()
	return Entry{ID: id, CrewID: crewID, AppID: appid, AddedBy: addedBy, AddedAt: ts, Status: StatusActive}, nil
}

// EntryByID loads one entry, scoped to its crew. A manual entry comes back with
// AppID 0 and Manual filled in; the payload is read in the same row, so loading
// a list never costs one query per entry.
func (s *Store) EntryByID(crewID, id int64) (Entry, error) {
	var (
		e   Entry
		raw string
	)
	err := s.db.QueryRow(`
		SELECT id, crew_id, COALESCE(appid, 0), COALESCE(added_by, 0), added_at, note, status, manual_json
		FROM entries WHERE crew_id = ? AND id = ?`, crewID, id).
		Scan(&e.ID, &e.CrewID, &e.AppID, &e.AddedBy, &e.AddedAt, &e.Note, &e.Status, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	if err != nil {
		return Entry{}, err
	}
	if e.Manual, err = decodeManual(raw); err != nil {
		return Entry{}, fmt.Errorf("entry %d: %w", id, err)
	}
	return e, nil
}

// Entries lists a crew's games with vote tallies and the viewer's own vote.
func (s *Store) Entries(crewID, viewer int64) ([]Entry, error) {
	rows, err := s.db.Query(`
		SELECT e.id, e.crew_id, COALESCE(e.appid, 0), COALESCE(e.added_by, 0), e.added_at, e.note, e.status,
		       COALESCE(SUM(CASE WHEN v.value > 0 THEN 1 ELSE 0 END), 0) AS ups,
		       COALESCE(SUM(CASE WHEN v.value < 0 THEN 1 ELSE 0 END), 0) AS downs,
		       COALESCE((SELECT w.value FROM votes w WHERE w.entry_id = e.id AND w.user_id = ?), 0) AS mine,
		       e.manual_json
		FROM entries e
		LEFT JOIN votes v ON v.entry_id = e.id
		WHERE e.crew_id = ?
		GROUP BY e.id
		ORDER BY e.id ASC`, viewer, crewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var (
			e   Entry
			raw string
		)
		if err := rows.Scan(&e.ID, &e.CrewID, &e.AppID, &e.AddedBy, &e.AddedAt, &e.Note, &e.Status,
			&e.Up, &e.Down, &e.MyVote, &raw); err != nil {
			return nil, err
		}
		if e.Manual, err = decodeManual(raw); err != nil {
			return nil, fmt.Errorf("entry %d: %w", e.ID, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteEntry takes a game off the list; its votes and manual ownership marks
// are removed with it in the same transaction, so no orphaned row can count
// toward a total later.
func (s *Store) DeleteEntry(crewID, id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM votes WHERE entry_id IN (SELECT id FROM entries WHERE crew_id = ? AND id = ?)", crewID, id); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM manual_ownership WHERE entry_id IN (SELECT id FROM entries WHERE crew_id = ? AND id = ?)", crewID, id); err != nil {
		return err
	}
	res, err := tx.Exec("DELETE FROM entries WHERE crew_id = ? AND id = ?", crewID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// SetEntryStatus moves an entry between "active" and "played".
func (s *Store) SetEntryStatus(crewID, id int64, status string) error {
	if status != StatusActive && status != StatusPlayed {
		return ErrInvalid
	}
	res, err := s.db.Exec("UPDATE entries SET status = ? WHERE crew_id = ? AND id = ?", status, crewID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetEntryNote stores the crew's short note about a game.
func (s *Store) SetEntryNote(crewID, id int64, note string) error {
	note = strings.TrimSpace(note)
	if len([]rune(note)) > 280 {
		note = string([]rune(note)[:280])
	}
	res, err := s.db.Exec("UPDATE entries SET note = ? WHERE crew_id = ? AND id = ?", note, crewID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Vote records a vote of +1 or -1. Value 0 withdraws the vote. The entry must
// belong to the given crew, and the voter must be a member of it.
func (s *Store) Vote(crewID, entryID, userID int64, value int) error {
	if value != 0 && value != 1 && value != -1 {
		return ErrInvalid
	}
	var crew int64
	err := s.db.QueryRow("SELECT crew_id FROM entries WHERE id = ?", entryID).Scan(&crew)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if crew != crewID {
		return ErrNotFound
	}
	var member int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM memberships WHERE crew_id = ? AND user_id = ?", crewID, userID).Scan(&member); err != nil {
		return err
	}
	if member == 0 {
		return ErrNotFound
	}
	if value == 0 {
		_, err = s.db.Exec("DELETE FROM votes WHERE entry_id = ? AND user_id = ?", entryID, userID)
		return err
	}
	_, err = s.db.Exec(`
		INSERT INTO votes (entry_id, user_id, value, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(entry_id, user_id) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		entryID, userID, value, now())
	return err
}

// --- ownership --------------------------------------------------------------

// SetOwned marks or unmarks a game as owned by a user.
func (s *Store) SetOwned(userID int64, appid int, owned bool, source string) error {
	if source != SourceLibrary {
		source = SourceManual
	}
	if owned {
		_, err := s.db.Exec(`
			INSERT INTO ownership (user_id, appid, source, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(user_id, appid) DO UPDATE SET source = excluded.source, updated_at = excluded.updated_at`,
			userID, appid, source, now())
		return err
	}
	// Unticking only removes a manual mark: a Steam-library import would
	// otherwise be silently forgotten the next time it refreshes.
	_, err := s.db.Exec("DELETE FROM ownership WHERE user_id = ? AND appid = ? AND source = ?", userID, appid, SourceManual)
	return err
}

// OwnedApps returns every app id a user is marked as owning.
func (s *Store) OwnedApps(userID int64) (map[int]bool, error) {
	rows, err := s.db.Query("SELECT appid FROM ownership WHERE user_id = ?", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int]bool)
	for rows.Next() {
		var appid int
		if err := rows.Scan(&appid); err != nil {
			return nil, err
		}
		out[appid] = true
	}
	return out, rows.Err()
}

// CrewOwnership returns appid -> set of member user ids who own it, restricted
// to members of one crew.
func (s *Store) CrewOwnership(crewID int64) (map[int]map[int64]bool, error) {
	rows, err := s.db.Query(`
		SELECT o.appid, o.user_id
		FROM ownership o
		JOIN memberships m ON m.user_id = o.user_id AND m.crew_id = ?`, crewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int]map[int64]bool)
	for rows.Next() {
		var appid int
		var uid int64
		if err := rows.Scan(&appid, &uid); err != nil {
			return nil, err
		}
		if out[appid] == nil {
			out[appid] = make(map[int64]bool, 4)
		}
		out[appid][uid] = true
	}
	return out, rows.Err()
}

// ReplaceLibrary overwrites a user's library-imported ownership with appids.
// Manual marks are left alone.
func (s *Store) ReplaceLibrary(userID int64, appids []int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM ownership WHERE user_id = ? AND source = ?", userID, SourceLibrary); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO ownership (user_id, appid, source, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, appid) DO UPDATE SET source = excluded.source, updated_at = excluded.updated_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	ts := now()
	for _, appid := range appids {
		if appid <= 0 {
			continue
		}
		if _, err := stmt.Exec(userID, appid, SourceLibrary, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// --- steam caches -----------------------------------------------------------

// CachedGame is a cached appdetails payload for one region.
type CachedGame struct {
	JSON      string
	FetchedAt int64
}

// GetGameCache returns the cached appdetails JSON for an app in a region.
func (s *Store) GetGameCache(appid int, country string) (CachedGame, error) {
	var c CachedGame
	err := s.db.QueryRow("SELECT json, fetched_at FROM game_cache WHERE appid = ? AND country = ?", appid, normalizeCountry(country)).
		Scan(&c.JSON, &c.FetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CachedGame{}, ErrNotFound
	}
	return c, err
}

// PutGameCache stores an appdetails payload.
func (s *Store) PutGameCache(appid int, country, json string, fetchedAt int64) error {
	_, err := s.db.Exec(`
		INSERT INTO game_cache (appid, country, json, fetched_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(appid, country) DO UPDATE SET json = excluded.json, fetched_at = excluded.fetched_at`,
		appid, normalizeCountry(country), json, fetchedAt)
	return err
}

// GetReviewCache returns the cached review summary for an app.
func (s *Store) GetReviewCache(appid int) (CachedGame, error) {
	var c CachedGame
	err := s.db.QueryRow("SELECT json, fetched_at FROM review_cache WHERE appid = ?", appid).Scan(&c.JSON, &c.FetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CachedGame{}, ErrNotFound
	}
	return c, err
}

// PutReviewCache stores a review summary.
func (s *Store) PutReviewCache(appid int, json string, fetchedAt int64) error {
	_, err := s.db.Exec(`
		INSERT INTO review_cache (appid, json, fetched_at) VALUES (?, ?, ?)
		ON CONFLICT(appid) DO UPDATE SET json = excluded.json, fetched_at = excluded.fetched_at`,
		appid, json, fetchedAt)
	return err
}
