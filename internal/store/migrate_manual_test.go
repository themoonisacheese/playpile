package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// legacySchema is the schema exactly as it shipped before manual entries:
// entries.appid was NOT NULL and there was no manual_json column. The migration
// test seeds a database with this text, so it is the real old shape.
const legacySchema = `CREATE TABLE IF NOT EXISTS users (
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
);`

func openRawDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(dir, "playpile.db") +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	db.SetMaxOpenConns(1)
	return db
}

// seedLegacy writes a database in the pre-manual shape, with entries, votes,
// membership and both sources of ownership.
func seedLegacy(t *testing.T, dir string) {
	t.Helper()
	db := openRawDB(t, dir)
	defer db.Close()

	if _, err := db.Exec(legacySchema); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO users (id, steam_id, display_name, avatar_url, profile_url, created_at, last_login_at)
		  VALUES (1, '76561197960287930', 'Ada', '', '', 1000, 1000)`, nil},
		{`INSERT INTO users (id, steam_id, display_name, avatar_url, profile_url, created_at, last_login_at)
		  VALUES (2, '76561197960287931', 'Bob', '', '', 1000, 1000)`, nil},
		{`INSERT INTO crews (id, name, country, language, created_by, created_at)
		  VALUES (1, 'Friday night', 'PL', 'english', 1, 1000)`, nil},
		{`INSERT INTO memberships (crew_id, user_id, role, joined_at) VALUES (1, 1, 'owner', 1000)`, nil},
		{`INSERT INTO memberships (crew_id, user_id, role, joined_at) VALUES (1, 2, 'member', 1001)`, nil},
		{`INSERT INTO entries (id, crew_id, appid, added_by, added_at, note, status)
		  VALUES (10, 1, 440, 1, 1100, '', 'active')`, nil},
		{`INSERT INTO entries (id, crew_id, appid, added_by, added_at, note, status)
		  VALUES (11, 1, 570, 2, 1101, 'worth a look', 'active')`, nil},
		{`INSERT INTO entries (id, crew_id, appid, added_by, added_at, note, status)
		  VALUES (12, 1, 730, 1, 1102, '', 'played')`, nil},
		{`INSERT INTO votes (entry_id, user_id, value, updated_at) VALUES (10, 1, 1, 1200)`, nil},
		{`INSERT INTO votes (entry_id, user_id, value, updated_at) VALUES (10, 2, -1, 1201)`, nil},
		{`INSERT INTO votes (entry_id, user_id, value, updated_at) VALUES (11, 2, 1, 1202)`, nil},
		{`INSERT INTO ownership (user_id, appid, source, updated_at) VALUES (1, 440, 'library', 1300)`, nil},
		{`INSERT INTO ownership (user_id, appid, source, updated_at) VALUES (2, 440, 'manual', 1301)`, nil},
		{`INSERT INTO ownership (user_id, appid, source, updated_at) VALUES (1, 570, 'manual', 1302)`, nil},
	}
	for _, st := range stmts {
		if _, err := db.Exec(st.sql, st.args...); err != nil {
			t.Fatalf("seed legacy data: %v", err)
		}
	}

	// Sanity check on the fixture itself: this must really be the old shape.
	rows, err := db.Query("PRAGMA table_info(entries)")
	if err != nil {
		t.Fatalf("pragma: %v", err)
	}
	defer rows.Close()
	appidNotNull := false
	hasManual := false
	for rows.Next() {
		var (
			cid, notNull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan pragma: %v", err)
		}
		if name == "appid" && notNull == 1 {
			appidNotNull = true
		}
		if name == "manual_json" {
			hasManual = true
		}
	}
	if !appidNotNull || hasManual {
		t.Fatalf("fixture is not the old schema: appidNotNull=%v hasManual=%v", appidNotNull, hasManual)
	}
}

func TestMigrateLegacySchemaPreservesEverything(t *testing.T) {
	dir := t.TempDir()
	seedLegacy(t, dir)

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}

	// Entry ids, appids, notes, statuses and vote tallies survive byte for byte.
	entries, err := s.Entries(1, 1)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	want := []struct {
		id     int64
		appid  int
		status string
		note   string
		up     int
		down   int
		mine   int
	}{
		{10, 440, StatusActive, "", 1, 1, 1},
		{11, 570, StatusActive, "worth a look", 1, 0, 0},
		{12, 730, StatusPlayed, "", 0, 0, 0},
	}
	for i, w := range want {
		e := entries[i]
		if e.ID != w.id || e.AppID != w.appid || e.Status != w.status || e.Note != w.note {
			t.Fatalf("entry %d = %+v, want %+v", i, e, w)
		}
		if e.Up != w.up || e.Down != w.down || e.MyVote != w.mine {
			t.Fatalf("entry %d tallies = up %d down %d mine %d, want %+v", e.ID, e.Up, e.Down, e.MyVote, w)
		}
		if e.Manual != nil {
			t.Fatalf("steam entry %d came back with a manual payload: %+v", e.ID, e.Manual)
		}
	}
	if e, err := s.EntryByID(1, 11); err != nil || e.Note != "worth a look" || e.AppID != 570 {
		t.Fatalf("EntryByID after migration = %+v, %v", e, err)
	}

	// Membership and ownership are untouched.
	members, err := s.Members(1)
	if err != nil || len(members) != 2 {
		t.Fatalf("members = %+v, %v", members, err)
	}
	owners, err := s.CrewOwnership(1)
	if err != nil {
		t.Fatalf("crew ownership: %v", err)
	}
	if !owners[440][1] || !owners[440][2] || !owners[570][1] || len(owners) != 2 {
		t.Fatalf("ownership after migration = %+v", owners)
	}
	if owned, _ := s.OwnedApps(1); !owned[440] || !owned[570] {
		t.Fatalf("OwnedApps after migration = %v", owned)
	}

	// A manual entry can now be added next to the old Steam entries, with no
	// invented appid.
	manual, err := s.AddManualEntry(1, ManualGame{Name: "Minecraft", Description: "blocks"}, 1)
	if err != nil {
		t.Fatalf("add manual entry after migration: %v", err)
	}
	if manual.AppID != 0 || manual.Manual == nil || manual.Manual.Name != "Minecraft" {
		t.Fatalf("manual entry = %+v", manual)
	}
	var rawAppID sql.NullInt64
	if err := s.db.QueryRow("SELECT appid FROM entries WHERE id = ?", manual.ID).Scan(&rawAppID); err != nil {
		t.Fatalf("read raw appid: %v", err)
	}
	if rawAppID.Valid {
		t.Fatalf("manual entry got a fake appid %d", rawAppID.Int64)
	}
	if err := s.SetManualOwned(1, manual.ID, 1, true); err != nil {
		t.Fatalf("own manual entry: %v", err)
	}
	if err := s.Vote(1, manual.ID, 2, -1); err != nil {
		t.Fatalf("veto manual entry: %v", err)
	}

	// Foreign keys must be back on for the pooled connection; otherwise every
	// later write would quietly skip them.
	var fk int
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys after migration = %d, %v", fk, err)
	}
	if _, err := s.db.Exec("INSERT INTO votes (entry_id, user_id, value, updated_at) VALUES (9999, 1, 1, 0)"); err == nil {
		t.Fatal("a vote for a missing entry was accepted: foreign keys are off")
	}
	if _, err := s.db.Exec("INSERT INTO entries (crew_id, appid, added_at, manual_json) VALUES (1, NULL, 1, '')"); err == nil {
		t.Fatal("an appid-less entry with no manual payload was accepted")
	}

	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopening must be a no-op migration: everything, including the manual
	// entry and its ownership, is still there.
	s, err = Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	entries, err = s.Entries(1, 1)
	if err != nil {
		t.Fatalf("entries after reopen: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("entries after reopen = %d, want 4", len(entries))
	}
	last := entries[3]
	if last.ID != manual.ID || last.Manual == nil || last.Manual.Name != "Minecraft" {
		t.Fatalf("manual entry after reopen = %+v (want id %d)", last, manual.ID)
	}
	if last.Down != 1 {
		t.Fatalf("manual entry votes after reopen = %+v", last)
	}
	manualOwners, err := s.CrewManualOwnership(1)
	if err != nil {
		t.Fatalf("manual ownership after reopen: %v", err)
	}
	if !manualOwners[manual.ID][1] {
		t.Fatalf("manual ownership after reopen = %+v", manualOwners)
	}
	if again, err := s.AddManualEntry(1, ManualGame{Name: "Minecraft"}, 1); err != nil || again.ID == 0 {
		t.Fatalf("second manual entry after reopen = %+v, %v", again, err)
	}
}

// TestMigrateRefusesBrokenForeignKeyAndRollsBack proves the rebuild is not a
// best-effort copy: a database whose rows would violate a foreign key after the
// rebuild makes Open fail, and the transaction leaves the old tables exactly as
// they were rather than half-migrated.
func TestMigrateRefusesBrokenForeignKeyAndRollsBack(t *testing.T) {
	dir := t.TempDir()
	seedLegacy(t, dir)

	// Simulate damage a previous tool could have left: a vote whose entry is
	// gone, only insertable with foreign keys off.
	db := openRawDB(t, dir)
	if _, err := db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatalf("disable fk: %v", err)
	}
	if _, err := db.Exec("INSERT INTO votes (entry_id, user_id, value, updated_at) VALUES (999, 1, 1, 0)"); err != nil {
		t.Fatalf("insert orphan vote: %v", err)
	}
	db.Close()

	if s, err := Open(dir); err == nil {
		s.Close()
		t.Fatal("Open accepted a database that would lose a foreign key target")
	}

	// The rollback kept the old shop: old shape, all entries, all votes.
	db = openRawDB(t, dir)
	defer db.Close()
	rows, err := db.Query("PRAGMA table_info(entries)")
	if err != nil {
		t.Fatalf("pragma: %v", err)
	}
	appidNotNull, hasManual := false, false
	for rows.Next() {
		var (
			cid, notNull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			t.Fatalf("scan pragma: %v", err)
		}
		if name == "appid" && notNull == 1 {
			appidNotNull = true
		}
		if name == "manual_json" {
			hasManual = true
		}
	}
	rows.Close()
	if !appidNotNull || hasManual {
		t.Fatalf("failed migration changed the table shape: appidNotNull=%v hasManual=%v", appidNotNull, hasManual)
	}
	var entries, votes, leftovers int
	if err := db.QueryRow("SELECT COUNT(*) FROM entries").Scan(&entries); err != nil || entries != 3 {
		t.Fatalf("entries after failed migration = %d, %v", entries, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM votes").Scan(&votes); err != nil || votes != 4 {
		t.Fatalf("votes after failed migration = %d, %v", votes, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name = 'entries_new'").Scan(&leftovers); err != nil || leftovers != 0 {
		t.Fatalf("entries_new leftover = %d, %v", leftovers, err)
	}

	// With the damage gone the same database migrates and keeps everything.
	if _, err := db.Exec("DELETE FROM votes WHERE entry_id = 999"); err != nil {
		t.Fatalf("remove orphan vote: %v", err)
	}
	db.Close()

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open after repair: %v", err)
	}
	defer s.Close()
	got, err := s.Entries(1, 1)
	if err != nil || len(got) != 3 {
		t.Fatalf("entries after repaired migration = %+v, %v", got, err)
	}
	if got[0].ID != 10 || got[1].ID != 11 || got[2].ID != 12 {
		t.Fatalf("ids after repaired migration = %d %d %d", got[0].ID, got[1].ID, got[2].ID)
	}
}
