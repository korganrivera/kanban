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
