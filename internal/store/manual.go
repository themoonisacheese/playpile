package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ManualGame is what a crew fills in for a game Steam does not sell.
//
// PriceMinor is integer minor units like every other price in the app: nil
// means "nobody knows the price yet" and 0 means free, which is why it is a
// pointer. MetaScore is nil when the crew left it blank. Currency only matters
// for a priced game.
type ManualGame struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	ImageURL    string `json:"image_url,omitempty"`
	StoreURL    string `json:"store_url,omitempty"`
	PriceMinor  *int64 `json:"price_minor,omitempty"`
	Currency    string `json:"currency,omitempty"`
	MetaScore   *int   `json:"metascore,omitempty"`
}

const (
	manualNameMax        = 200
	manualDescriptionMax = 2000
	manualURLMax         = 2048
	manualPriceMax       = int64(1_000_000_000_000)
)

// normalizeManualGame trims and checks a manual entry, returning the value to
// store. Anything questionable is refused rather than silently changed, so a
// bad edit leaves the stored row exactly as it was.
func normalizeManualGame(g ManualGame) (ManualGame, error) {
	name := strings.TrimSpace(g.Name)
	if n := len([]rune(name)); n == 0 || n > manualNameMax {
		return ManualGame{}, ErrInvalid
	}
	desc := strings.TrimSpace(g.Description)
	if len([]rune(desc)) > manualDescriptionMax {
		return ManualGame{}, ErrInvalid
	}
	imageURL, err := normalizeManualURL(g.ImageURL)
	if err != nil {
		return ManualGame{}, err
	}
	storeURL, err := normalizeManualURL(g.StoreURL)
	if err != nil {
		return ManualGame{}, err
	}
	currency := strings.ToUpper(strings.TrimSpace(g.Currency))
	if g.PriceMinor != nil {
		if *g.PriceMinor < 0 || *g.PriceMinor > manualPriceMax {
			return ManualGame{}, ErrInvalid
		}
		if !validCurrencyCode(currency) {
			return ManualGame{}, ErrInvalid
		}
	} else if currency != "" && !validCurrencyCode(currency) {
		return ManualGame{}, ErrInvalid
	}
	if g.MetaScore != nil && (*g.MetaScore < 0 || *g.MetaScore > 100) {
		return ManualGame{}, ErrInvalid
	}

	out := ManualGame{Name: name, Description: desc, ImageURL: imageURL, StoreURL: storeURL, Currency: currency}
	if g.PriceMinor != nil {
		p := *g.PriceMinor
		out.PriceMinor = &p
	}
	if g.MetaScore != nil {
		m := *g.MetaScore
		out.MetaScore = &m
	}
	return out, nil
}

// normalizeManualURL accepts an empty URL or an absolute http(s) URL without
// embedded credentials, and returns it trimmed.
func normalizeManualURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if len(raw) > manualURLMax {
		return "", ErrInvalid
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", ErrInvalid
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return "", ErrInvalid
	}
	if u.Host == "" || u.User != nil {
		return "", ErrInvalid
	}
	return raw, nil
}

func validCurrencyCode(code string) bool {
	if len(code) != 3 {
		return false
	}
	for _, r := range code {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

func encodeManual(g ManualGame) (string, error) {
	b, err := json.Marshal(g)
	if err != nil {
		return "", fmt.Errorf("encode manual game: %w", err)
	}
	return string(b), nil
}

// decodeManual turns the stored payload into a value; "" is a Steam entry.
func decodeManual(raw string) (*ManualGame, error) {
	if raw == "" {
		return nil, nil
	}
	var g ManualGame
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		return nil, fmt.Errorf("decode manual game: %w", err)
	}
	return &g, nil
}

func (s *Store) checkCrewExists(crewID int64) error {
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM crews WHERE id = ?", crewID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) checkUserExists(userID int64) error {
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM users WHERE id = ?", userID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddManualEntry puts a game Steam does not sell on a crew's list. Its appid is
// NULL, never a made-up Steam id, and its details live in manual_json. A crew
// may list the same manual game twice (two different Minecraft editions, say),
// because SQLite's unique index treats NULL appids as distinct.
func (s *Store) AddManualEntry(crewID int64, game ManualGame, addedBy int64) (Entry, error) {
	if crewID <= 0 {
		return Entry{}, ErrInvalid
	}
	g, err := normalizeManualGame(game)
	if err != nil {
		return Entry{}, err
	}
	raw, err := encodeManual(g)
	if err != nil {
		return Entry{}, err
	}
	if err := s.checkCrewExists(crewID); err != nil {
		return Entry{}, err
	}
	var added any
	if addedBy > 0 {
		if err := s.checkUserExists(addedBy); err != nil {
			return Entry{}, err
		}
		added = addedBy
	}
	ts := now()
	res, err := s.db.Exec(`
		INSERT INTO entries (crew_id, appid, added_by, added_at, status, manual_json)
		VALUES (?, NULL, ?, ?, ?, ?)`, crewID, added, ts, StatusActive, raw)
	if err != nil {
		return Entry{}, fmt.Errorf("add manual entry: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Entry{}, err
	}
	manual := g
	return Entry{ID: id, CrewID: crewID, AppID: 0, AddedBy: addedBy, AddedAt: ts, Status: StatusActive, Manual: &manual}, nil
}

// UpdateManualEntry replaces the details of a manual entry. Steam entries are
// refused: their data belongs to the catalog cache, not to a person.
func (s *Store) UpdateManualEntry(crewID, entryID int64, game ManualGame) error {
	if crewID <= 0 || entryID <= 0 {
		return ErrInvalid
	}
	g, err := normalizeManualGame(game)
	if err != nil {
		return err
	}
	raw, err := encodeManual(g)
	if err != nil {
		return err
	}
	var appid sql.NullInt64
	err = s.db.QueryRow("SELECT appid FROM entries WHERE crew_id = ? AND id = ?", crewID, entryID).Scan(&appid)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if appid.Valid {
		return ErrInvalid
	}
	res, err := s.db.Exec("UPDATE entries SET manual_json = ? WHERE crew_id = ? AND id = ?", raw, crewID, entryID)
	if err != nil {
		return fmt.Errorf("update manual entry: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetManualOwned marks or unmarks a manual game as owned by a crew member.
// Beyond the entry and the crew, this is the same ownership idea as for Steam
// games, so the "already owns it" maths works the same way.
func (s *Store) SetManualOwned(crewID, entryID, userID int64, owned bool) error {
	if crewID <= 0 || entryID <= 0 || userID <= 0 {
		return ErrInvalid
	}
	var appid sql.NullInt64
	err := s.db.QueryRow("SELECT appid FROM entries WHERE crew_id = ? AND id = ?", crewID, entryID).Scan(&appid)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if appid.Valid {
		return ErrInvalid
	}
	var member int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM memberships WHERE crew_id = ? AND user_id = ?", crewID, userID).Scan(&member); err != nil {
		return err
	}
	if member == 0 {
		return ErrNotFound
	}
	if owned {
		_, err = s.db.Exec(`
			INSERT INTO manual_ownership (entry_id, user_id) VALUES (?, ?)
			ON CONFLICT(entry_id, user_id) DO NOTHING`, entryID, userID)
		return err
	}
	_, err = s.db.Exec("DELETE FROM manual_ownership WHERE entry_id = ? AND user_id = ?", entryID, userID)
	return err
}

// CrewManualOwnership returns entry id -> set of member user ids who own it.
// Like CrewOwnership it counts only current members, so a removed member's mark
// stops counting immediately.
func (s *Store) CrewManualOwnership(crewID int64) (map[int64]map[int64]bool, error) {
	rows, err := s.db.Query(`
		SELECT mo.entry_id, mo.user_id
		FROM manual_ownership mo
		JOIN entries e ON e.id = mo.entry_id AND e.crew_id = ?
		JOIN memberships m ON m.user_id = mo.user_id AND m.crew_id = e.crew_id`, crewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]map[int64]bool)
	for rows.Next() {
		var entryID, userID int64
		if err := rows.Scan(&entryID, &userID); err != nil {
			return nil, err
		}
		if out[entryID] == nil {
			out[entryID] = make(map[int64]bool, 4)
		}
		out[entryID][userID] = true
	}
	return out, rows.Err()
}
