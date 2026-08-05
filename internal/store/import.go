package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"kanban-go/internal/board"
	"kanban-go/internal/legacy"
)

var ErrDestinationNotEmpty = errors.New("destination contains kanban data; use replace mode explicitly")

type LegacyImportReport struct {
	Tasks             int      `json:"tasks"`
	Users             int      `json:"users"`
	CompletionEntries int      `json:"completionEntries"`
	UndoImported      int      `json:"undoImported"`
	UndoSkipped       int      `json:"undoSkipped"`
	WIPLimits         int      `json:"wipLimits"`
	Warnings          []string `json:"warnings"`
}

// ImportLegacy replaces the destination in one transaction. A nonempty
// destination is rejected unless replace is explicitly enabled.
func (store *Store) ImportLegacy(ctx context.Context, bundle *legacy.Bundle, replace bool) (*LegacyImportReport, error) {
	if bundle == nil {
		return nil, errors.New("legacy bundle is required")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	count, err := contentCount(ctx, tx)
	if err != nil {
		return nil, err
	}
	if count > 0 && !replace {
		return nil, ErrDestinationNotEmpty
	}
	if replace {
		if err := clearImportedData(ctx, tx); err != nil {
			return nil, err
		}
	}

	report := &LegacyImportReport{Warnings: append([]string(nil), bundle.Report.Warnings...)}
	for _, user := range bundle.Users {
		if _, err := tx.ExecContext(ctx, `
				INSERT INTO users(username, password_hash, created_at, password_changed_at)
				VALUES (?, ?, ?, ?)`,
			user.Username, nullableString(user.PasswordHash),
			formatTime(user.CreatedAt), timeValue(user.PasswordChangedAt),
		); err != nil {
			return nil, fmt.Errorf("import user %q: %w", user.Username, err)
		}
		report.Users++
	}

	entryByCompletion := make(map[string]string)
	for _, user := range bundle.Users {
		for index, entry := range user.History {
			entryID := legacyID("completion", user.Username, fmt.Sprint(index), formatTime(entry.OccurredAt), entry.TaskID, entry.CompletionID)
			if _, err := tx.ExecContext(ctx, `
					INSERT INTO completion_entries(id, username, task_id, task_title, completed_at)
					VALUES (?, ?, ?, ?, ?)`,
				entryID, user.Username, entry.TaskID, entry.TaskTitle, formatTime(entry.OccurredAt),
			); err != nil {
				return nil, fmt.Errorf("import completion entry for %q: %w", user.Username, err)
			}
			if entry.CompletionID != "" {
				entryByCompletion[user.Username+"\x00"+entry.CompletionID] = entryID
			}
			report.CompletionEntries++
		}
	}

	for _, task := range bundle.Tasks {
		lifecycle, claimedBy, claimedAt := importedTaskState(task.State, task.Picker, task.PickedAt)
		if task.State == "InProgress" && claimedBy == nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("task %q was InProgress without an owner; imported as Ready", task.ID))
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tasks(
				id, title, description, lifecycle, scheduled_at, deadline, lead_days,
					recurrence_kind, recurrence_days, recurrence_paused, time_critical,
					remedy_for, created_by, claimed_by, claimed_at, block_note,
					last_completed_at, created_at, updated_at, version
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, 1)`,
			task.ID, task.Title, task.Description, lifecycle, timeValue(task.ScheduledAt),
			timeValue(task.Deadline), task.LeadDays, task.Recurrence.Kind,
			task.Recurrence.Days, boolInt(task.Recurrence.Paused), boolInt(task.TimeCritical),
			stringValue(task.CreatedBy), stringValue(claimedBy), timeValue(claimedAt),
			task.BlockNote, timeValue(task.LastCompleted),
			formatTime(task.CreatedAt), formatTime(task.UpdatedAt),
		); err != nil {
			return nil, fmt.Errorf("import task %q: %w", task.ID, err)
		}
		report.Tasks++
	}

	for _, task := range bundle.Tasks {
		if task.RemedyFor != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE tasks SET remedy_for = ? WHERE id = ?`, *task.RemedyFor, task.ID); err != nil {
				return nil, fmt.Errorf("import remedy reference for task %q: %w", task.ID, err)
			}
		}
		for _, dependencyID := range task.Dependencies {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO task_dependencies(task_id, depends_on_id) VALUES (?, ?)`, task.ID, dependencyID); err != nil {
				return nil, fmt.Errorf("import dependency %q -> %q: %w", task.ID, dependencyID, err)
			}
		}
		for _, weekday := range task.Recurrence.Weekdays {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO task_recurrence_weekdays(task_id, weekday) VALUES (?, ?)`, task.ID, weekday); err != nil {
				return nil, fmt.Errorf("import recurrence weekday for task %q: %w", task.ID, err)
			}
		}
	}

	for _, task := range bundle.Tasks {
		if task.CompletionUndo == nil {
			continue
		}
		undo := task.CompletionUndo
		if !task.UpdatedAt.Equal(undo.CompletedAt) {
			report.UndoSkipped++
			report.Warnings = append(report.Warnings, fmt.Sprintf("task %q changed after its last completion; completion undo was not imported", task.ID))
			continue
		}
		var completionEntryID any
		if undo.CompletionUser != "" {
			entryID := entryByCompletion[undo.CompletionUser+"\x00"+undo.CompletionID]
			if entryID == "" {
				report.UndoSkipped++
				report.Warnings = append(report.Warnings, fmt.Sprintf("task %q has no exact completion history entry for its undo; undo was not imported", task.ID))
				continue
			}
			completionEntryID = entryID
		}
		previousLifecycle, previousClaimedBy, previousClaimedAt := importedTaskState(
			undo.Previous.State, undo.Previous.Picker, undo.Previous.PickedAt,
		)
		actor := undo.CompletionUser
		if actor == "" && task.Picker != nil {
			actor = *task.Picker
		}
		if actor == "" {
			actor = "legacy"
		}
		occurrenceID := legacyID("occurrence", task.ID, undo.CompletionID, formatTime(undo.CompletedAt))
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO task_occurrences(
				id, task_id, outcome, actor, occurred_at, scheduled_for,
				previous_lifecycle, previous_scheduled_at, previous_claimed_by,
				previous_claimed_at, previous_last_completed_at, result_version,
				completion_entry_id
			) VALUES (?, ?, 'completed', ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
			occurrenceID, task.ID, actor, formatTime(undo.CompletedAt),
			timeValue(undo.Previous.ScheduledAt), previousLifecycle,
			timeValue(undo.Previous.ScheduledAt), stringValue(previousClaimedBy),
			timeValue(previousClaimedAt), timeValue(undo.Previous.LastCompleted),
			completionEntryID,
		); err != nil {
			return nil, fmt.Errorf("import completion undo for task %q: %w", task.ID, err)
		}
		report.UndoImported++
	}

	for state, limit := range bundle.Limits {
		result, err := tx.ExecContext(ctx, `UPDATE wip_limits SET limit_count = ? WHERE state = ?`, intValue(limit), state)
		if err != nil {
			return nil, fmt.Errorf("import WIP limit for %q: %w", state, err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, fmt.Errorf("unknown WIP state %q", state)
		}
		report.WIPLimits++
	}
	if err := validateImportedData(ctx, tx, report); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return report, nil
}

func contentCount(ctx context.Context, tx *sql.Tx) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM tasks) +
		       (SELECT COUNT(*) FROM users) +
		       (SELECT COUNT(*) FROM completion_entries)`).Scan(&count)
	return count, err
}

func clearImportedData(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{
		"sessions", "task_occurrences", "task_dependencies",
		"tasks", "completion_entries", "users",
	} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}
	return nil
}

func importedTaskState(state string, picker *string, pickedAt *time.Time) (board.Lifecycle, *string, *time.Time) {
	switch state {
	case "Done":
		return board.LifecycleDone, picker, pickedAt
	case "InProgress":
		if picker != nil && strings.TrimSpace(*picker) != "" {
			return board.LifecycleInProgress, picker, pickedAt
		}
	case "Blocked":
		return board.LifecycleBlocked, nil, nil
	}
	return board.LifecycleReady, nil, nil
}

func legacyID(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "legacy-" + hex.EncodeToString(digest[:16])
}

func validateImportedData(ctx context.Context, tx *sql.Tx, report *LegacyImportReport) error {
	checks := []struct {
		name  string
		query string
		want  int
	}{
		{"tasks", `SELECT COUNT(*) FROM tasks`, report.Tasks},
		{"users", `SELECT COUNT(*) FROM users`, report.Users},
		{"completion entries", `SELECT COUNT(*) FROM completion_entries`, report.CompletionEntries},
	}
	for _, check := range checks {
		var got int
		if err := tx.QueryRowContext(ctx, check.query).Scan(&got); err != nil {
			return err
		}
		if got != check.want {
			return fmt.Errorf("post-import %s count is %d, want %d", check.name, got, check.want)
		}
	}
	var badClaims int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tasks
		WHERE (lifecycle NOT IN ('InProgress', 'Done') AND claimed_by IS NOT NULL)
		   OR (lifecycle = 'InProgress' AND (claimed_by IS NULL OR claimed_by = ''))`).Scan(&badClaims); err != nil {
		return err
	}
	if badClaims != 0 {
		return fmt.Errorf("post-import invariant failed: %d tasks have invalid claim ownership", badClaims)
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("post-import foreign key check failed")
	}
	return rows.Err()
}
