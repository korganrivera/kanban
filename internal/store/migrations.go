package store

import (
	"database/sql"
	"fmt"
)

var migrations = []string{
	`ALTER TABLE tasks ADD COLUMN time_critical INTEGER NOT NULL DEFAULT 0 CHECK (time_critical IN (0, 1));
	 ALTER TABLE tasks ADD COLUMN remedy_for TEXT REFERENCES tasks(id) ON DELETE SET NULL;
	 CREATE TABLE wip_limits (
		state TEXT PRIMARY KEY CHECK (state IN ('Waiting', 'Ready', 'InProgress', 'Blocked', 'Suspended', 'Done')),
		limit_count INTEGER CHECK (limit_count IS NULL OR limit_count >= 0)
	 );
	 INSERT INTO wip_limits(state, limit_count) VALUES
		('Waiting', NULL), ('Ready', NULL), ('InProgress', 4),
		('Blocked', 10), ('Suspended', NULL), ('Done', NULL);`,
	`CREATE TABLE task_recurrence_weekdays (
		task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		weekday INTEGER NOT NULL CHECK (weekday BETWEEN 0 AND 6),
		PRIMARY KEY (task_id, weekday)
	);`,
	`CREATE TABLE users (
		username TEXT PRIMARY KEY,
		password_hash TEXT,
		points INTEGER NOT NULL DEFAULT 0 CHECK (points >= 0),
		created_at TEXT NOT NULL,
		password_changed_at TEXT
	);
	 CREATE TABLE sessions (
		token_hash TEXT PRIMARY KEY,
		username TEXT NOT NULL REFERENCES users(username) ON DELETE CASCADE,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL
	);
	 CREATE INDEX idx_sessions_expiry ON sessions(expires_at);
	 CREATE INDEX idx_sessions_user ON sessions(username);
	 ALTER TABLE tasks ADD COLUMN created_by TEXT REFERENCES users(username) ON DELETE SET NULL;`,
	`ALTER TABLE tasks ADD COLUMN points_snapshot INTEGER CHECK (points_snapshot IS NULL OR points_snapshot >= 0);
	 ALTER TABLE tasks ADD COLUMN points_snapshot_by TEXT REFERENCES users(username) ON DELETE SET NULL;
	 ALTER TABLE tasks ADD COLUMN points_snapshot_at TEXT;
	 ALTER TABLE tasks ADD COLUMN unclaimed_at TEXT;
	 CREATE TABLE task_point_snapshots (
		id TEXT PRIMARY KEY,
		task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		username TEXT NOT NULL REFERENCES users(username) ON DELETE CASCADE,
		points INTEGER NOT NULL CHECK (points >= 0),
		created_at TEXT NOT NULL
	 );
	 CREATE INDEX idx_task_point_snapshots_task ON task_point_snapshots(task_id, created_at DESC);
	 CREATE TABLE point_entries (
		id TEXT PRIMARY KEY,
		username TEXT NOT NULL REFERENCES users(username) ON DELETE CASCADE,
		task_id TEXT NOT NULL,
		task_title TEXT NOT NULL,
		points INTEGER NOT NULL CHECK (points >= 0),
		reason TEXT NOT NULL,
		occurred_at TEXT NOT NULL,
		reversed_at TEXT
	 );
	 CREATE INDEX idx_point_entries_user_time ON point_entries(username, occurred_at DESC);
	 CREATE INDEX idx_point_entries_task ON point_entries(task_id);
	 ALTER TABLE task_occurrences ADD COLUMN award_entry_id TEXT REFERENCES point_entries(id);
	 ALTER TABLE task_occurrences ADD COLUMN previous_points_snapshot INTEGER;
	 ALTER TABLE task_occurrences ADD COLUMN previous_points_snapshot_by TEXT;
	 ALTER TABLE task_occurrences ADD COLUMN previous_points_snapshot_at TEXT;
	 ALTER TABLE task_occurrences ADD COLUMN previous_unclaimed_at TEXT;`,
	`ALTER TABLE tasks ADD COLUMN deadline TEXT;`,
	`DROP INDEX idx_point_entries_user_time;
	 DROP INDEX idx_point_entries_task;
	 ALTER TABLE point_entries RENAME TO completion_entries;
	 ALTER TABLE completion_entries DROP COLUMN points;
	 ALTER TABLE completion_entries DROP COLUMN reason;
	 ALTER TABLE completion_entries RENAME COLUMN occurred_at TO completed_at;
	 CREATE INDEX idx_completion_entries_user_time ON completion_entries(username, completed_at DESC);
	 CREATE INDEX idx_completion_entries_task ON completion_entries(task_id);
	 ALTER TABLE task_occurrences RENAME COLUMN award_entry_id TO completion_entry_id;
	 ALTER TABLE task_occurrences DROP COLUMN previous_points_snapshot;
	 ALTER TABLE task_occurrences DROP COLUMN previous_points_snapshot_by;
	 ALTER TABLE task_occurrences DROP COLUMN previous_points_snapshot_at;
	 ALTER TABLE task_occurrences DROP COLUMN previous_unclaimed_at;
	 DROP TABLE task_point_snapshots;
	 ALTER TABLE tasks DROP COLUMN points_snapshot;
	 ALTER TABLE tasks DROP COLUMN points_snapshot_by;
	 ALTER TABLE tasks DROP COLUMN points_snapshot_at;
	 ALTER TABLE tasks DROP COLUMN unclaimed_at;
	 ALTER TABLE users DROP COLUMN points;`,
}

func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version, len(migrations))
	}
	for version < len(migrations) {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[version]); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply database migration %d: %w", version+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", version+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		version++
	}
	return nil
}
