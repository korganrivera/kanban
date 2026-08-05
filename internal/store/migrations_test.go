package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestPointSchemaMigratesToScorelessCompletionHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-points.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	for index, migration := range migrations[:5] {
		if _, err := db.Exec(migration); err != nil {
			t.Fatalf("apply old migration %d: %v", index+1, err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", index+1)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`
		INSERT INTO users(username, password_hash, points, created_at)
		VALUES ('alice', NULL, 7, '2026-08-05T12:00:00Z');
		INSERT INTO point_entries(id, username, task_id, task_title, points, reason, occurred_at)
		VALUES ('entry-1', 'alice', 'deleted-task', 'Legacy completion', 7, 'legacy', '2026-08-05T12:00:00Z');
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	entries, err := database.CompletionHistory(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].TaskTitle != "Legacy completion" {
		t.Fatalf("migrated completion history = %#v", entries)
	}
	for _, removed := range []string{"points", "points_snapshot", "task_point_snapshots", "point_entries"} {
		var count int
		if err := database.db.QueryRow(`
			SELECT COUNT(*) FROM sqlite_schema
			WHERE (type = 'table' OR type = 'index') AND name = ?`, removed).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("removed schema object %q still exists", removed)
		}
	}
	assertColumnMissing(t, database.db, "users", "points")
	assertColumnMissing(t, database.db, "tasks", "points_snapshot")
}

func assertColumnMissing(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == column {
			t.Fatalf("column %s.%s still exists", table, column)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
