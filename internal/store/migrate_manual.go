package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// migrateEntriesManual relaxes entries.appid to nullable and adds manual_json.
//
// SQLite cannot drop a NOT NULL constraint in place, so this is the documented
// table rebuild: create the new shape, copy every row with its id, drop the old
// table, rename, then check that no foreign key points at something that is
// gone. Votes reference entries(id) and manual ownership rows are keyed by
// entry_id, so foreign key enforcement is turned off for the rebuild alone:
// with it on, DROP TABLE entries would cascade-delete every vote. The pragma is
// restored on the same connection before it goes back to the pool.
//
// It runs only when the table still has the old shape, so opening an
// already-migrated database (or reopening the same one) does nothing.
func (s *Store) migrateEntriesManual() error {
	rows, err := s.db.Query("PRAGMA table_info(entries)")
	if err != nil {
		return err
	}
	var (
		appidPresent bool
		appidNotNull bool
		hasManual    bool
	)
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		switch name {
		case "appid":
			appidPresent = true
			appidNotNull = notNull != 0
		case "manual_json":
			hasManual = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if !appidPresent {
		return fmt.Errorf("entries table has no appid column")
	}
	if hasManual && !appidNotNull {
		return nil
	}

	ctx := context.Background()
	// A dedicated connection: the foreign_keys pragma is per connection, and
	// it must be the connection that runs the rebuild and then gets it back.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	var on int
	restore := func() error {
		if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
			return err
		}
		if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&on); err != nil {
			return err
		}
		if on != 1 {
			return fmt.Errorf("foreign_keys stayed off after migration")
		}
		return nil
	}
	// Restoring on the error paths keeps a half-failed open from poisoning the
	// pooled connection; a failed restore is reported because every later write
	// would otherwise silently skip its foreign keys.
	defer func() { _ = restore() }()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS entries_new"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, entriesNewTable); err != nil {
		return err
	}

	cols := []string{"id", "crew_id", "appid", "added_by", "added_at", "note", "status"}
	src := []string{"id", "crew_id", "appid", "added_by", "added_at", "note", "status"}
	if hasManual {
		cols = append(cols, "manual_json")
		src = append(src, "COALESCE(manual_json, '')")
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO entries_new ("+strings.Join(cols, ", ")+") SELECT "+strings.Join(src, ", ")+" FROM entries"); err != nil {
		return fmt.Errorf("copy entries: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DROP TABLE entries"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "ALTER TABLE entries_new RENAME TO entries"); err != nil {
		return err
	}

	check, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	violations := 0
	for check.Next() {
		var (
			table  string
			rowid  sql.NullInt64
			parent string
			fkid   int
		)
		if err := check.Scan(&table, &rowid, &parent, &fkid); err != nil {
			check.Close()
			return err
		}
		violations++
	}
	if err := check.Err(); err != nil {
		check.Close()
		return err
	}
	check.Close()
	if violations > 0 {
		return fmt.Errorf("entries migration would leave %d foreign key violations", violations)
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	return restore()
}
