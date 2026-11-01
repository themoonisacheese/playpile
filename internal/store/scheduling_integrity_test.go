package store

import (
	"encoding/json"
	"testing"
	"time"
)

// Start with the actual pre-scheduling schema, not an empty modern database.
// Every existing table must retain exactly the same row values after repeated upgrades.
func TestSchedulingUpgradePreservesPopulatedDatabase(t *testing.T) {
	dir := t.TempDir()
	old := &Store{db: openRawDB(t, dir)}
	if _, err := old.db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	owner := newUser(t, old, "upgrade-owner", "Owner")
	crew, err := old.CreateCrew("Existing crew", "US", "english", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	steam, err := old.AddEntry(crew.ID, 440, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	manual, err := old.AddManualEntry(crew.ID, ManualGame{Name: "Existing manual game"}, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []Entry{steam, manual} {
		if err := old.Vote(crew.ID, entry.ID, owner.ID, 1); err != nil {
			t.Fatal(err)
		}
		if err := old.SetEntryNote(crew.ID, entry.ID, "Keep this note"); err != nil {
			t.Fatal(err)
		}
	}
	if err := old.SetOwned(owner.ID, 440, true, SourceManual); err != nil {
		t.Fatal(err)
	}
	if err := old.SetManualOwned(crew.ID, manual.ID, owner.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := old.CreateSession(owner.ID, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := old.CreateInvite(crew.ID, owner.ID, RoleMember, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := old.PutGameCache(440, "US", `{"name":"Cached"}`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if err := old.PutReviewCache(440, `{"description":"Positive"}`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	tables := []string{"users", "sessions", "crews", "memberships", "invites", "entries", "votes", "ownership", "manual_ownership", "game_cache", "review_cache"}
	snapshot := func(s *Store) map[string]string {
		t.Helper()
		out := map[string]string{}
		for _, table := range tables {
			rows, err := s.db.Query("SELECT * FROM " + table + " ORDER BY rowid")
			if err != nil {
				t.Fatal(err)
			}
			cols, err := rows.Columns()
			if err != nil {
				t.Fatal(err)
			}
			var contents [][]any
			for rows.Next() {
				values := make([]any, len(cols))
				targets := make([]any, len(cols))
				for i := range values {
					targets[i] = &values[i]
				}
				if err := rows.Scan(targets...); err != nil {
					t.Fatal(err)
				}
				contents = append(contents, values)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			data, err := json.Marshal(contents)
			if err != nil {
				t.Fatal(err)
			}
			out[table] = string(data)
		}
		return out
	}
	before := snapshot(old)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		upgraded, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		after := snapshot(upgraded)
		for _, table := range tables {
			if after[table] != before[table] {
				t.Errorf("upgrade %d changed %s rows\nbefore=%s\nafter=%s", pass, table, before[table], after[table])
			}
		}
		if _, err := upgraded.PlanningMembers(crew.ID, owner.ID); err != nil {
			t.Fatal(err)
		}
		if err := upgraded.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
