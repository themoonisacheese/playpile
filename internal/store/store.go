// Package store is playpile's persistence layer: SQLite through the pure-Go
// modernc driver, one file, no cgo.
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	// ErrNotFound means the row does not exist (or is not visible in this scope).
	ErrNotFound = errors.New("store: not found")
	// ErrExists means a unique constraint would be violated.
	ErrExists = errors.New("store: already exists")
	// ErrInvalid means the caller passed something the store refuses to write.
	ErrInvalid = errors.New("store: invalid value")
)

// Roles, ordered from most to least powerful.
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// Rank orders roles; higher can do everything lower can.
func Rank(role string) int {
	switch role {
	case RoleOwner:
		return 3
	case RoleAdmin:
		return 2
	case RoleMember:
		return 1
	}
	return 0
}

// ValidRole reports whether role is one of the known roles.
func ValidRole(role string) bool { return Rank(role) > 0 }

// Entry statuses.
const (
	StatusActive = "active"
	StatusPlayed = "played"
)

// Ownership sources.
const (
	SourceManual  = "manual"
	SourceLibrary = "library"
)

// Store owns the database handle.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database inside dir.
func Open(dir string) (*Store, error) {
	path := filepath.Join(dir, "playpile.db")
	dsn := "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// One connection keeps every write serialised, which removes a whole class
	// of "database is locked" bugs. Traffic here is a handful of friends.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id            INTEGER PRIMARY KEY,
	steam_id      TEXT    NOT NULL UNIQUE,
	display_name  TEXT    NOT NULL,
	avatar_url    TEXT    NOT NULL DEFAULT '',
	profile_url   TEXT    NOT NULL DEFAULT '',
	created_at    INTEGER NOT NULL,
	last_login_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
	id         TEXT    PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	csrf       TEXT    NOT NULL,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_expires ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS crews (
	id         INTEGER PRIMARY KEY,
	name       TEXT    NOT NULL,
	country    TEXT    NOT NULL DEFAULT 'US',
	language   TEXT    NOT NULL DEFAULT 'english',
	created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS memberships (
	crew_id   INTEGER NOT NULL REFERENCES crews(id) ON DELETE CASCADE,
	user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	role      TEXT    NOT NULL DEFAULT 'member',
	joined_at INTEGER NOT NULL,
	PRIMARY KEY (crew_id, user_id)
);

CREATE TABLE IF NOT EXISTS invites (
	token      TEXT    PRIMARY KEY,
	crew_id    INTEGER NOT NULL REFERENCES crews(id) ON DELETE CASCADE,
	created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
	role       TEXT    NOT NULL DEFAULT 'member',
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL DEFAULT 0,
	max_uses   INTEGER NOT NULL DEFAULT 0,
	uses       INTEGER NOT NULL DEFAULT 0,
	revoked_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS invites_crew ON invites(crew_id);

CREATE TABLE IF NOT EXISTS entries (
	id       INTEGER PRIMARY KEY,
	crew_id  INTEGER NOT NULL REFERENCES crews(id) ON DELETE CASCADE,
	appid    INTEGER NOT NULL,
	added_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
	added_at INTEGER NOT NULL,
	note     TEXT    NOT NULL DEFAULT '',
	status   TEXT    NOT NULL DEFAULT 'active',
	UNIQUE (crew_id, appid)
);

CREATE TABLE IF NOT EXISTS votes (
	entry_id   INTEGER NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	value      INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (entry_id, user_id)
);

CREATE TABLE IF NOT EXISTS ownership (
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	appid      INTEGER NOT NULL,
	source     TEXT    NOT NULL DEFAULT 'manual',
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (user_id, appid)
);

CREATE TABLE IF NOT EXISTS game_cache (
	appid      INTEGER NOT NULL,
	country    TEXT    NOT NULL,
	json       TEXT    NOT NULL,
	fetched_at INTEGER NOT NULL,
	PRIMARY KEY (appid, country)
);

CREATE TABLE IF NOT EXISTS review_cache (
	appid      INTEGER PRIMARY KEY,
	json       TEXT    NOT NULL,
	fetched_at INTEGER NOT NULL
);
`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// User is somebody who signed in with Steam.
type User struct {
	ID          int64
	SteamID     string
	DisplayName string
	AvatarURL   string
	ProfileURL  string
	CreatedAt   int64
	LastLoginAt int64
}

// Crew is a group of people with one shared game list.
type Crew struct {
	ID        int64
	Name      string
	Country   string
	Language  string
	CreatedBy int64
	CreatedAt int64
}

// Membership ties a user to a crew with a role.
type Membership struct {
	CrewID   int64
	UserID   int64
	Role     string
	JoinedAt int64
}

// Can reports whether this membership may act at the given permission level.
func (m Membership) Can(need string) bool { return Rank(m.Role) >= Rank(need) }

// Member is a membership joined with its user.
type Member struct {
	User
	Role     string
	JoinedAt int64
}

// Session is one browser login.
type Session struct {
	ID        string
	UserID    int64
	CSRF      string
	CreatedAt int64
	ExpiresAt int64
}

// Invite is a shareable link that grants membership.
type Invite struct {
	Token     string
	CrewID    int64
	CreatedBy int64
	Role      string
	CreatedAt int64
	ExpiresAt int64
	MaxUses   int
	Uses      int
	RevokedAt int64
}

// Expires reports whether the invite has an expiry that has passed.
func (i Invite) Expired(now int64) bool { return i.ExpiresAt > 0 && now >= i.ExpiresAt }

// Exhausted reports whether the invite hit its use limit.
func (i Invite) Exhausted() bool { return i.MaxUses > 0 && i.Uses >= i.MaxUses }

// Usable reports whether the invite can still be redeemed.
func (i Invite) Usable(now int64) bool {
	return i.RevokedAt == 0 && !i.Expired(now) && !i.Exhausted()
}

// Entry is one game on a crew's list, with its vote tallies for one viewer.
type Entry struct {
	ID      int64
	CrewID  int64
	AppID   int
	AddedBy int64
	AddedAt int64
	Note    string
	Status  string
	Up      int
	Down    int
	MyVote  int
}

// Score is the net vote count.
func (e Entry) Score() int { return e.Up - e.Down }

// now returns the current unix time; tests and callers use it for consistency.
func now() int64 { return time.Now().Unix() }

// randomToken returns n random bytes as unpadded base64url.
func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// normalizeName trims a display/crew name and rejects empty results.
func normalizeName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ErrInvalid
	}
	if len([]rune(s)) > 80 {
		s = string([]rune(s)[:80])
	}
	return s, nil
}
