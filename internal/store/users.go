package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const userCols = "id, steam_id, display_name, avatar_url, profile_url, created_at, last_login_at"

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.SteamID, &u.DisplayName, &u.AvatarURL, &u.ProfileURL, &u.CreatedAt, &u.LastLoginAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// UpsertSteamUser records a login, creating the user on first sight. Empty
// avatar/profile values keep whatever was stored before.
func (s *Store) UpsertSteamUser(steamID, displayName, avatarURL, profileURL string) (User, error) {
	steamID = strings.TrimSpace(steamID)
	if steamID == "" {
		return User{}, ErrInvalid
	}
	displayName = strings.TrimSpace(displayName)
	ts := now()

	if existing, err := s.UserBySteamID(steamID); err == nil {
		// Only overwrite what Steam actually told us about this time.
		if _, err := s.db.Exec(`
			UPDATE users SET
				last_login_at = ?,
				display_name  = CASE WHEN ? <> '' THEN ? ELSE display_name END,
				avatar_url    = CASE WHEN ? <> '' THEN ? ELSE avatar_url   END,
				profile_url   = CASE WHEN ? <> '' THEN ? ELSE profile_url  END
			WHERE id = ?`,
			ts, displayName, displayName, avatarURL, avatarURL, profileURL, profileURL, existing.ID); err != nil {
			return User{}, fmt.Errorf("upsert user: %w", err)
		}
		return s.UserByID(existing.ID)
	} else if !errors.Is(err, ErrNotFound) {
		return User{}, err
	}

	if displayName == "" {
		suffix := steamID
		if len(suffix) > 4 {
			suffix = suffix[len(suffix)-4:]
		}
		displayName = "Player " + suffix
	}
	_, err := s.db.Exec(`
		INSERT INTO users (steam_id, display_name, avatar_url, profile_url, created_at, last_login_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		steamID, displayName, avatarURL, profileURL, ts, ts)
	if err != nil {
		return User{}, fmt.Errorf("upsert user: %w", err)
	}
	return s.UserBySteamID(steamID)
}

// UserBySteamID looks a user up by their SteamID64.
func (s *Store) UserBySteamID(steamID string) (User, error) {
	return scanUser(s.db.QueryRow("SELECT "+userCols+" FROM users WHERE steam_id = ?", steamID))
}

// UserByID looks a user up by local id.
func (s *Store) UserByID(id int64) (User, error) {
	return scanUser(s.db.QueryRow("SELECT "+userCols+" FROM users WHERE id = ?", id))
}

// SetDisplayName overrides the name shown in the app.
func (s *Store) SetDisplayName(userID int64, name string) error {
	name, err := normalizeName(name)
	if err != nil {
		return err
	}
	res, err := s.db.Exec("UPDATE users SET display_name = ? WHERE id = ?", name, userID)
	if err != nil {
		return fmt.Errorf("set display name: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- sessions ---------------------------------------------------------------

// CreateSession issues a new browser session with its own CSRF token.
func (s *Store) CreateSession(userID int64, ttl time.Duration) (Session, error) {
	id, err := randomToken(32)
	if err != nil {
		return Session{}, err
	}
	csrf, err := randomToken(32)
	if err != nil {
		return Session{}, err
	}
	ts := now()
	sess := Session{ID: id, UserID: userID, CSRF: csrf, CreatedAt: ts, ExpiresAt: ts + int64(ttl.Seconds())}
	_, err = s.db.Exec("INSERT INTO sessions (id, user_id, csrf, created_at, expires_at) VALUES (?, ?, ?, ?, ?)",
		sess.ID, sess.UserID, sess.CSRF, sess.CreatedAt, sess.ExpiresAt)
	if err != nil {
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	return sess, nil
}

// SessionByID returns a live session, or ErrNotFound when it is missing or expired.
func (s *Store) SessionByID(id string) (Session, error) {
	var sess Session
	err := s.db.QueryRow("SELECT id, user_id, csrf, created_at, expires_at FROM sessions WHERE id = ?", id).
		Scan(&sess.ID, &sess.UserID, &sess.CSRF, &sess.CreatedAt, &sess.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	if sess.ExpiresAt <= now() {
		return Session{}, ErrNotFound
	}
	return sess, nil
}

// DeleteSession logs one browser out.
func (s *Store) DeleteSession(id string) error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE id = ?", id)
	return err
}

// PurgeExpiredSessions deletes sessions past their expiry.
func (s *Store) PurgeExpiredSessions() error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE expires_at <= ?", now())
	return err
}

// --- crews ------------------------------------------------------------------

// CreateCrew creates a crew with the creator as owner.
func (s *Store) CreateCrew(name, country, language string, owner int64) (Crew, error) {
	name, err := normalizeName(name)
	if err != nil {
		return Crew{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Crew{}, err
	}
	defer tx.Rollback()

	ts := now()
	res, err := tx.Exec("INSERT INTO crews (name, country, language, created_by, created_at) VALUES (?, ?, ?, ?, ?)",
		name, normalizeCountry(country), normalizeLanguage(language), owner, ts)
	if err != nil {
		return Crew{}, fmt.Errorf("create crew: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Crew{}, err
	}
	if _, err := tx.Exec("INSERT INTO memberships (crew_id, user_id, role, joined_at) VALUES (?, ?, ?, ?)",
		id, owner, RoleOwner, ts); err != nil {
		return Crew{}, fmt.Errorf("create crew: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Crew{}, err
	}
	return Crew{ID: id, Name: name, Country: normalizeCountry(country), Language: normalizeLanguage(language), CreatedBy: owner, CreatedAt: ts}, nil
}

// CrewByID loads one crew.
func (s *Store) CrewByID(id int64) (Crew, error) {
	var c Crew
	err := s.db.QueryRow("SELECT id, name, country, language, COALESCE(created_by, 0), created_at FROM crews WHERE id = ?", id).
		Scan(&c.ID, &c.Name, &c.Country, &c.Language, &c.CreatedBy, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Crew{}, ErrNotFound
	}
	return c, err
}

// CrewCard is a crew plus the counts needed to list it on the home page.
type CrewCard struct {
	Crew
	Members int
	Entries int
}

// CrewsForUser lists the crews a user belongs to, most recently joined first.
func (s *Store) CrewsForUser(userID int64) ([]CrewCard, error) {
	rows, err := s.db.Query(`
		SELECT c.id, c.name, c.country, c.language, COALESCE(c.created_by, 0), c.created_at,
		       (SELECT COUNT(*) FROM memberships m2 WHERE m2.crew_id = c.id),
		       (SELECT COUNT(*) FROM entries e WHERE e.crew_id = c.id AND e.status = 'active')
		FROM crews c
		JOIN memberships m ON m.crew_id = c.id AND m.user_id = ?
		ORDER BY m.joined_at DESC, c.id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CrewCard
	for rows.Next() {
		var cc CrewCard
		if err := rows.Scan(&cc.ID, &cc.Name, &cc.Country, &cc.Language, &cc.CreatedBy, &cc.CreatedAt, &cc.Members, &cc.Entries); err != nil {
			return nil, err
		}
		out = append(out, cc)
	}
	return out, rows.Err()
}

// UpdateCrew renames a crew and/or changes its price region.
func (s *Store) UpdateCrew(id int64, name, country, language string) error {
	name, err := normalizeName(name)
	if err != nil {
		return err
	}
	res, err := s.db.Exec("UPDATE crews SET name = ?, country = ?, language = ? WHERE id = ?",
		name, normalizeCountry(country), normalizeLanguage(language), id)
	if err != nil {
		return fmt.Errorf("update crew: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteCrew removes a crew, its entries, votes and invites.
func (s *Store) DeleteCrew(id int64) error {
	_, err := s.db.Exec("DELETE FROM crews WHERE id = ?", id)
	return err
}

// Membership returns the caller's role in a crew, or ErrNotFound if not a member.
func (s *Store) Membership(crewID, userID int64) (Membership, error) {
	var m Membership
	err := s.db.QueryRow("SELECT crew_id, user_id, role, joined_at FROM memberships WHERE crew_id = ? AND user_id = ?", crewID, userID).
		Scan(&m.CrewID, &m.UserID, &m.Role, &m.JoinedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Membership{}, ErrNotFound
	}
	return m, err
}

// Members lists a crew's members, most senior first.
func (s *Store) Members(crewID int64) ([]Member, error) {
	rows, err := s.db.Query(`
		SELECT u.id, u.steam_id, u.display_name, u.avatar_url, u.profile_url, u.created_at, u.last_login_at, m.role, m.joined_at
		FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.crew_id = ?
		ORDER BY CASE m.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 ELSE 2 END, u.display_name`, crewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.ID, &m.SteamID, &m.DisplayName, &m.AvatarURL, &m.ProfileURL,
			&m.CreatedAt, &m.LastLoginAt, &m.Role, &m.JoinedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MemberCount counts a crew's members.
func (s *Store) MemberCount(crewID int64) (int, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM memberships WHERE crew_id = ?", crewID).Scan(&n)
	return n, err
}

// SetMemberRole changes somebody's role. Callers enforce who may do this.
func (s *Store) SetMemberRole(crewID, userID int64, role string) error {
	if !ValidRole(role) {
		return ErrInvalid
	}
	res, err := s.db.Exec("UPDATE memberships SET role = ? WHERE crew_id = ? AND user_id = ?", role, crewID, userID)
	if err != nil {
		return fmt.Errorf("set role: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RemoveMember drops somebody from a crew. Their votes go with them.
func (s *Store) RemoveMember(crewID, userID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM votes WHERE user_id = ? AND entry_id IN (SELECT id FROM entries WHERE crew_id = ?)`,
		userID, crewID); err != nil {
		return err
	}
	res, err := tx.Exec("DELETE FROM memberships WHERE crew_id = ? AND user_id = ?", crewID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// --- invites ----------------------------------------------------------------

// CreateInvite mints an invite link. maxUses 0 means unlimited; expiresAt 0 means never.
func (s *Store) CreateInvite(crewID, createdBy int64, role string, expiresAt int64, maxUses int) (Invite, error) {
	if !ValidRole(role) || role == RoleOwner {
		return Invite{}, ErrInvalid
	}
	if maxUses < 0 {
		return Invite{}, ErrInvalid
	}
	token, err := randomToken(18)
	if err != nil {
		return Invite{}, err
	}
	inv := Invite{Token: token, CrewID: crewID, CreatedBy: createdBy, Role: role, CreatedAt: now(), ExpiresAt: expiresAt, MaxUses: maxUses}
	_, err = s.db.Exec(`
		INSERT INTO invites (token, crew_id, created_by, role, created_at, expires_at, max_uses, uses)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0)`,
		inv.Token, inv.CrewID, inv.CreatedBy, inv.Role, inv.CreatedAt, inv.ExpiresAt, inv.MaxUses)
	if err != nil {
		return Invite{}, fmt.Errorf("create invite: %w", err)
	}
	return inv, nil
}

const inviteCols = "token, crew_id, COALESCE(created_by, 0), role, created_at, expires_at, max_uses, uses, revoked_at"

func scanInvite(row interface{ Scan(...any) error }) (Invite, error) {
	var i Invite
	err := row.Scan(&i.Token, &i.CrewID, &i.CreatedBy, &i.Role, &i.CreatedAt, &i.ExpiresAt, &i.MaxUses, &i.Uses, &i.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Invite{}, ErrNotFound
	}
	return i, err
}

// InviteByToken loads an invite by its token.
func (s *Store) InviteByToken(token string) (Invite, error) {
	return scanInvite(s.db.QueryRow("SELECT "+inviteCols+" FROM invites WHERE token = ?", token))
}

// InvitesForCrew lists the invites of a crew, newest first.
func (s *Store) InvitesForCrew(crewID int64) ([]Invite, error) {
	rows, err := s.db.Query("SELECT "+inviteCols+" FROM invites WHERE crew_id = ? ORDER BY created_at DESC", crewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// RevokeInvite kills an invite link.
func (s *Store) RevokeInvite(crewID int64, token string) error {
	res, err := s.db.Exec("UPDATE invites SET revoked_at = ? WHERE token = ? AND crew_id = ? AND revoked_at = 0", now(), token, crewID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RedeemInvite adds the user to the invited crew and returns its id. Redeeming
// while already a member is a no-op that still succeeds.
func (s *Store) RedeemInvite(token string, userID int64) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	inv, err := scanInvite(tx.QueryRow("SELECT "+inviteCols+" FROM invites WHERE token = ?", token))
	if err != nil {
		return 0, err
	}
	if !inv.Usable(now()) {
		return 0, ErrNotFound
	}
	res, err := tx.Exec("INSERT OR IGNORE INTO memberships (crew_id, user_id, role, joined_at) VALUES (?, ?, ?, ?)",
		inv.CrewID, userID, inv.Role, now())
	if err != nil {
		return 0, fmt.Errorf("redeem invite: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if _, err := tx.Exec("UPDATE invites SET uses = uses + 1 WHERE token = ?", token); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inv.CrewID, nil
}

func normalizeCountry(country string) string {
	country = strings.ToUpper(strings.TrimSpace(country))
	if len(country) != 2 {
		return "US"
	}
	for _, r := range country {
		if r < 'A' || r > 'Z' {
			return "US"
		}
	}
	return country
}

func normalizeLanguage(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" || len(lang) > 32 {
		return "english"
	}
	for _, r := range lang {
		if !(r >= 'a' && r <= 'z') && r != ' ' && r != '-' {
			return "english"
		}
	}
	return lang
}
