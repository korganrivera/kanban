package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// BackupTo writes a transactionally consistent standalone SQLite database.
// The destination must not already exist.
func (store *Store) BackupTo(ctx context.Context, destination string) error {
	absDestination, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if absDestination == store.path {
		return errors.New("backup destination cannot be the live database")
	}
	if _, err := os.Stat(absDestination); err == nil {
		return fmt.Errorf("backup destination already exists: %s", absDestination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absDestination), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(absDestination), 0o700); err != nil {
		return err
	}
	if _, err := store.db.ExecContext(ctx, `VACUUM INTO ?`, absDestination); err != nil {
		return fmt.Errorf("create SQLite backup: %w", err)
	}
	if err := os.Chmod(absDestination, 0o600); err != nil {
		return err
	}
	return nil
}
