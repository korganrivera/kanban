package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kanban-go/internal/board"

	_ "modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	path string
	now  func() time.Time
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func Open(path string) (*Store, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(absPath), 0o700); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", filepath.ToSlash(absPath))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize database: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	if err := os.Chmod(absPath, 0o600); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, path: absPath, now: time.Now}, nil
}

func (store *Store) Close() error {
	return store.db.Close()
}

func (store *Store) List(ctx context.Context) ([]*board.Task, error) {
	tasks, err := listTasks(ctx, store.db)
	if err != nil {
		return nil, err
	}
	board.DeriveStates(tasks, store.now().UTC())
	board.ComputePriorities(tasks, store.now().UTC())
	return tasks, nil
}

func (store *Store) Get(ctx context.Context, id string) (*board.Task, error) {
	tasks, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	return findTask(tasks, id)
}

func (store *Store) Create(ctx context.Context, input board.TaskInput) (*board.Task, error) {
	if err := input.Normalize(); err != nil {
		return nil, err
	}
	now := store.now().UTC()
	id, err := newID()
	if err != nil {
		return nil, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := ensureDependenciesExist(ctx, tx, id, input.Dependencies); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
        INSERT INTO tasks (
			id, title, description, lifecycle, scheduled_at, deadline, lead_days,
            recurrence_kind, recurrence_days, recurrence_paused,
			time_critical, created_by, created_at, updated_at, version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		id, input.Title, input.Description, board.LifecycleReady,
		timeValue(input.ScheduledAt), timeValue(input.Deadline), input.LeadDays, input.Recurrence.Kind,
		input.Recurrence.Days, boolInt(input.Recurrence.Paused),
		boolInt(input.TimeCritical), stringValue(input.CreatedBy),
		formatTime(now), formatTime(now),
	)
	if err != nil {
		return nil, err
	}
	if err := replaceDependencies(ctx, tx, id, input.Dependencies); err != nil {
		return nil, err
	}
	if err := replaceRecurrenceWeekdays(ctx, tx, id, input.Recurrence.Weekdays); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return store.Get(ctx, id)
}

func (store *Store) Update(ctx context.Context, id string, input board.TaskInput) (*board.Task, error) {
	if err := input.Normalize(); err != nil {
		return nil, err
	}
	if input.Version < 1 {
		return nil, errors.New("version is required")
	}
	for _, dependencyID := range input.Dependencies {
		if dependencyID == id {
			return nil, errors.New("task cannot depend on itself")
		}
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	allTasks, err := listTasks(ctx, tx)
	if err != nil {
		return nil, err
	}
	existing, err := findTask(allTasks, id)
	if err != nil {
		return nil, err
	}
	if err := ensureDependenciesExist(ctx, tx, id, input.Dependencies); err != nil {
		return nil, err
	}
	if wouldCreateCycle(allTasks, id, input.Dependencies) {
		return nil, errors.New("dependencies would create a cycle")
	}
	now := store.now().UTC()
	result, err := tx.ExecContext(ctx, `
        UPDATE tasks SET
			title = ?, description = ?, block_note = ?, scheduled_at = ?, deadline = ?, lead_days = ?,
			recurrence_kind = ?, recurrence_days = ?, recurrence_paused = ?, time_critical = ?,
            updated_at = ?, version = version + 1
        WHERE id = ? AND version = ?`,
		input.Title, input.Description, input.BlockNote, timeValue(input.ScheduledAt), timeValue(input.Deadline), input.LeadDays,
		input.Recurrence.Kind, input.Recurrence.Days, boolInt(input.Recurrence.Paused),
		boolInt(input.TimeCritical),
		formatTime(now), id, input.Version,
	)
	if err != nil {
		return nil, err
	}
	if err := requireChanged(result); err != nil {
		return nil, err
	}
	if err := replaceDependencies(ctx, tx, id, input.Dependencies); err != nil {
		return nil, err
	}
	if err := cleanupRemovedRemedies(ctx, tx, existing, input.Dependencies, input.CleanupRemedies); err != nil {
		return nil, err
	}
	if err := replaceRecurrenceWeekdays(ctx, tx, id, input.Recurrence.Weekdays); err != nil {
		return nil, err
	}
	updatedTasks, err := listTasks(ctx, tx)
	if err != nil {
		return nil, err
	}
	board.DeriveStates(updatedTasks, now)
	updated, err := findTask(updatedTasks, id)
	if err != nil {
		return nil, err
	}
	if updated.Lifecycle == board.LifecycleInProgress && updated.EffectiveState != board.StateInProgress {
		if _, err := tx.ExecContext(ctx, `
            UPDATE tasks SET lifecycle = ?, claimed_by = NULL, claimed_at = NULL,
				unclaimed_at = ?, updated_at = ?, version = version + 1 WHERE id = ?`,
			board.LifecycleReady, formatTime(now), formatTime(now), id,
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return store.Get(ctx, id)
}

func (store *Store) Action(ctx context.Context, id, action, actor string, input board.ActionInput) (*board.Task, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = "local"
	}
	if input.Version < 1 {
		return nil, errors.New("version is required")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	tasks, err := listTasks(ctx, tx)
	if err != nil {
		return nil, err
	}
	now := store.now().UTC()
	board.DeriveStates(tasks, now)
	board.ComputePriorities(tasks, now)
	task, err := findTask(tasks, id)
	if err != nil {
		return nil, err
	}
	if task.Version != input.Version {
		return nil, board.ErrConflict
	}

	switch action {
	case "claim":
		if task.EffectiveState != board.StateReady {
			return nil, board.ErrInvalidAction
		}
		if err := enforceWIP(ctx, tx, tasks, board.StateInProgress, task.ID, now); err != nil {
			return nil, err
		}
		err = claimTask(ctx, tx, task, actor, now)
	case "release":
		if task.EffectiveState != board.StateInProgress {
			return nil, board.ErrInvalidAction
		}
		if err := enforceWIP(ctx, tx, tasks, board.StateReady, task.ID, now); err != nil {
			return nil, err
		}
		err = updateLifecycle(ctx, tx, task, board.LifecycleReady, nil, nil, "", now)
	case "block":
		if task.EffectiveState != board.StateReady && task.EffectiveState != board.StateInProgress && task.EffectiveState != board.StateBlocked {
			return nil, board.ErrInvalidAction
		}
		note := strings.TrimSpace(input.Note)
		if len(note) > 5000 {
			return nil, errors.New("block note must be 5000 characters or fewer")
		}
		if task.EffectiveState != board.StateBlocked {
			if err := enforceWIP(ctx, tx, tasks, board.StateBlocked, task.ID, now); err != nil {
				return nil, err
			}
		}
		err = updateLifecycle(ctx, tx, task, board.LifecycleBlocked, nil, nil, note, now)
	case "unblock":
		if task.EffectiveState != board.StateBlocked {
			return nil, board.ErrInvalidAction
		}
		if err := enforceWIP(ctx, tx, tasks, board.StateReady, task.ID, now); err != nil {
			return nil, err
		}
		err = updateLifecycle(ctx, tx, task, board.LifecycleReady, nil, nil, "", now)
	case "complete":
		if task.EffectiveState != board.StateInProgress {
			return nil, board.ErrInvalidAction
		}
		if err := enforceWIP(ctx, tx, tasks, board.StateDone, task.ID, now); err != nil {
			return nil, err
		}
		err = completeTask(ctx, tx, task, actor, now)
	case "undo":
		err = undoCompletion(ctx, tx, task, now)
	default:
		return nil, board.ErrInvalidAction
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return store.Get(ctx, id)
}

func (store *Store) WIPLimits(ctx context.Context) (board.WIPLimits, error) {
	return listWIPLimits(ctx, store.db)
}

func (store *Store) UpdateWIPLimits(ctx context.Context, updates board.WIPLimits) (board.WIPLimits, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for state, limit := range updates {
		if !board.ValidState(state) {
			return nil, fmt.Errorf("unknown WIP state %q", state)
		}
		if limit != nil && *limit < 0 {
			return nil, fmt.Errorf("%s WIP limit must be non-negative or null", state)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE wip_limits SET limit_count = ? WHERE state = ?`, intValue(limit), state); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return store.WIPLimits(ctx)
}

func (store *Store) CreateRemedy(ctx context.Context, id, actor string, input board.RemedyInput) (*board.RemedyResult, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	tasks, err := listTasks(ctx, tx)
	if err != nil {
		return nil, err
	}
	now := store.now().UTC()
	board.DeriveStates(tasks, now)
	parent, err := findTask(tasks, id)
	if err != nil {
		return nil, err
	}
	if err := input.Normalize(parent.Title); err != nil {
		return nil, err
	}
	if parent.Version != input.Version {
		return nil, board.ErrConflict
	}
	if parent.EffectiveState != board.StateBlocked {
		return nil, board.ErrInvalidAction
	}

	remedyID, err := newID()
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO tasks (
			id, title, description, lifecycle, scheduled_at, deadline, lead_days,
			recurrence_kind, recurrence_days, recurrence_paused, time_critical,
			remedy_for, created_by, created_at, updated_at, version
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'none', 0, 0, 0, ?, ?, ?, ?, 1)`,
		remedyID, input.Title, input.Description, board.LifecycleReady,
		timeValue(parent.ScheduledAt), timeValue(firstTime(input.Deadline, parent.Deadline)), parent.LeadDays,
		parent.ID, nullableString(actor),
		formatTime(now), formatTime(now),
	)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO task_dependencies(task_id, depends_on_id) VALUES (?, ?)`, parent.ID, remedyID); err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE tasks SET lifecycle = ?, claimed_by = NULL, claimed_at = NULL,
			block_note = '', updated_at = ?, version = version + 1
		WHERE id = ? AND version = ?`,
		board.LifecycleReady, formatTime(now), parent.ID, parent.Version,
	)
	if err != nil {
		return nil, err
	}
	if err := requireChanged(result); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	blockedTask, err := store.Get(ctx, parent.ID)
	if err != nil {
		return nil, err
	}
	remedyTask, err := store.Get(ctx, remedyID)
	if err != nil {
		return nil, err
	}
	return &board.RemedyResult{BlockedTask: blockedTask, RemedyTask: remedyTask}, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func firstTime(preferred, fallback *time.Time) *time.Time {
	if preferred != nil {
		return preferred
	}
	return fallback
}

func (store *Store) Delete(ctx context.Context, id string, input board.DeleteInput) ([]board.TaskReference, error) {
	if input.Version < 1 {
		return nil, errors.New("version is required")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM tasks WHERE id = ?`, id).Scan(&version); errors.Is(err, sql.ErrNoRows) {
		return nil, board.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	if version != input.Version {
		return nil, board.ErrConflict
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT t.id, t.title
		FROM tasks t
		JOIN task_dependencies d ON d.task_id = t.id
		WHERE d.depends_on_id = ?
		ORDER BY t.created_at, t.id`, id)
	if err != nil {
		return nil, err
	}
	dependents := make([]board.TaskReference, 0)
	for rows.Next() {
		var dependent board.TaskReference
		if err := rows.Scan(&dependent.ID, &dependent.Title); err != nil {
			rows.Close()
			return nil, err
		}
		dependents = append(dependents, dependent)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(dependents) > 0 && !input.Force {
		return dependents, board.ErrHasDependents
	}

	if len(dependents) > 0 {
		now := formatTime(store.now().UTC())
		if _, err := tx.ExecContext(ctx, `
			UPDATE tasks SET updated_at = ?, version = version + 1
			WHERE id IN (SELECT task_id FROM task_dependencies WHERE depends_on_id = ?)`, now, id); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_dependencies WHERE depends_on_id = ?`, id); err != nil {
			return nil, err
		}
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE id = ? AND version = ?`, id, input.Version)
	if err != nil {
		return nil, err
	}
	if err := requireChanged(result); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dependents, nil
}

func updateLifecycle(ctx context.Context, tx *sql.Tx, task *board.Task, lifecycle board.Lifecycle, claimedBy *string, claimedAt *time.Time, blockNote string, now time.Time) error {
	unclaimedAt := task.UnclaimedAt
	if task.Lifecycle == board.LifecycleInProgress && lifecycle != board.LifecycleInProgress {
		unclaimedAt = &now
	}
	if lifecycle == board.LifecycleInProgress {
		unclaimedAt = nil
	}
	result, err := tx.ExecContext(ctx, `
        UPDATE tasks SET lifecycle = ?, claimed_by = ?, claimed_at = ?, block_note = ?,
			unclaimed_at = ?, updated_at = ?, version = version + 1
        WHERE id = ? AND version = ?`,
		lifecycle, stringValue(claimedBy), timeValue(claimedAt), blockNote,
		timeValue(unclaimedAt), formatTime(now), task.ID, task.Version,
	)
	if err != nil {
		return err
	}
	return requireChanged(result)
}

func claimTask(ctx context.Context, tx *sql.Tx, task *board.Task, actor string, now time.Time) error {
	if err := ensurePointUser(ctx, tx, actor, now); err != nil {
		return err
	}
	refreshSnapshot := task.PointsSnapshot == nil || task.SnapshotBy == nil || *task.SnapshotBy != actor
	if task.UnclaimedAt != nil && now.Sub(*task.UnclaimedAt) >= 24*time.Hour {
		refreshSnapshot = true
	}
	points := task.PointsSnapshot
	snapshotBy := task.SnapshotBy
	snapshotAt := task.SnapshotAt
	if refreshSnapshot {
		value := task.Priority
		points = &value
		snapshotBy = &actor
		snapshotAt = &now
		snapshotID, err := newID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO task_point_snapshots(id, task_id, username, points, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			snapshotID, task.ID, actor, value, formatTime(now),
		); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE tasks SET lifecycle = ?, claimed_by = ?, claimed_at = ?, block_note = '',
			points_snapshot = ?, points_snapshot_by = ?, points_snapshot_at = ?,
			unclaimed_at = NULL, updated_at = ?, version = version + 1
		WHERE id = ? AND version = ?`,
		board.LifecycleInProgress, actor, formatTime(now), intValue(points), stringValue(snapshotBy),
		timeValue(snapshotAt), formatTime(now), task.ID, task.Version,
	)
	if err != nil {
		return err
	}
	return requireChanged(result)
}

func ensurePointUser(ctx context.Context, tx *sql.Tx, username string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO users(username, password_hash, points, created_at)
		VALUES (?, NULL, 0, ?) ON CONFLICT(username) DO NOTHING`,
		username, formatTime(now),
	)
	return err
}

func completeTask(ctx context.Context, tx *sql.Tx, task *board.Task, actor string, now time.Time) error {
	occurrenceID, err := newID()
	if err != nil {
		return err
	}
	awardID, err := newID()
	if err != nil {
		return err
	}
	nextSchedule, err := board.AdvanceSchedule(task, now)
	if err != nil {
		return err
	}
	newLifecycle := board.LifecycleDone
	newClaimedBy := task.ClaimedBy
	newClaimedAt := task.ClaimedAt
	if newClaimedBy == nil {
		newClaimedBy = &actor
		newClaimedAt = &now
	}
	awardOwner := *newClaimedBy
	if err := ensurePointUser(ctx, tx, awardOwner, now); err != nil {
		return err
	}
	awardPoints := 0
	if task.PointsSnapshot != nil {
		awardPoints = *task.PointsSnapshot
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO point_entries(
			id, username, task_id, task_title, points, reason, occurred_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		awardID, awardOwner, task.ID, task.Title, awardPoints,
		"Completed task "+task.ID+" ("+task.Title+")", formatTime(now),
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET points = points + ? WHERE username = ?`, awardPoints, awardOwner); err != nil {
		return err
	}

	newSnapshot := task.PointsSnapshot
	newSnapshotBy := task.SnapshotBy
	newSnapshotAt := task.SnapshotAt
	newUnclaimedAt := task.UnclaimedAt
	if task.Recurrence.Kind != "none" {
		newLifecycle = board.LifecycleReady
		newClaimedBy = nil
		newClaimedAt = nil
		newSnapshot = nil
		newSnapshotBy = nil
		newSnapshotAt = nil
		newUnclaimedAt = nil
	}
	resultVersion := task.Version + 1
	_, err = tx.ExecContext(ctx, `
        INSERT INTO task_occurrences (
            id, task_id, outcome, actor, occurred_at, scheduled_for,
            previous_lifecycle, previous_scheduled_at, previous_claimed_by,
			previous_claimed_at, previous_last_completed_at, result_version,
			award_entry_id, previous_points_snapshot, previous_points_snapshot_by,
			previous_points_snapshot_at, previous_unclaimed_at
		) VALUES (?, ?, 'completed', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		occurrenceID, task.ID, actor, formatTime(now), timeValue(task.ScheduledAt),
		task.Lifecycle, timeValue(task.ScheduledAt), stringValue(task.ClaimedBy),
		timeValue(task.ClaimedAt), timeValue(task.LastCompletedAt), resultVersion,
		awardID, intValue(task.PointsSnapshot), stringValue(task.SnapshotBy),
		timeValue(task.SnapshotAt), timeValue(task.UnclaimedAt),
	)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
        UPDATE tasks SET lifecycle = ?, scheduled_at = ?, claimed_by = ?, claimed_at = ?,
			points_snapshot = ?, points_snapshot_by = ?, points_snapshot_at = ?,
			unclaimed_at = ?, block_note = '', last_completed_at = ?, updated_at = ?, version = ?
        WHERE id = ? AND version = ?`,
		newLifecycle, timeValue(nextSchedule), stringValue(newClaimedBy), timeValue(newClaimedAt),
		intValue(newSnapshot), stringValue(newSnapshotBy), timeValue(newSnapshotAt), timeValue(newUnclaimedAt),
		formatTime(now), formatTime(now), resultVersion, task.ID, task.Version,
	)
	if err != nil {
		return err
	}
	return requireChanged(result)
}

func undoCompletion(ctx context.Context, tx *sql.Tx, task *board.Task, now time.Time) error {
	var occurrenceID string
	var previousLifecycle string
	var awardEntryID, previousScheduled, previousClaimedBy, previousClaimedAt sql.NullString
	var previousLastCompleted, previousSnapshotBy, previousSnapshotAt, previousUnclaimedAt sql.NullString
	var previousSnapshot sql.NullInt64
	err := tx.QueryRowContext(ctx, `
        SELECT id, previous_lifecycle, previous_scheduled_at, previous_claimed_by,
			previous_claimed_at, previous_last_completed_at, award_entry_id,
			previous_points_snapshot, previous_points_snapshot_by,
			previous_points_snapshot_at, previous_unclaimed_at
        FROM task_occurrences
        WHERE task_id = ? AND outcome = 'completed' AND undone_at IS NULL AND result_version = ?
        ORDER BY occurred_at DESC LIMIT 1`, task.ID, task.Version,
	).Scan(
		&occurrenceID, &previousLifecycle, &previousScheduled, &previousClaimedBy,
		&previousClaimedAt, &previousLastCompleted, &awardEntryID, &previousSnapshot,
		&previousSnapshotBy, &previousSnapshotAt, &previousUnclaimedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return board.ErrInvalidAction
	}
	if err != nil {
		return err
	}
	if awardEntryID.Valid {
		var username string
		var points int
		var reversedAt sql.NullString
		err := tx.QueryRowContext(ctx, `
			SELECT username, points, reversed_at FROM point_entries WHERE id = ?`, awardEntryID.String,
		).Scan(&username, &points, &reversedAt)
		if errors.Is(err, sql.ErrNoRows) || reversedAt.Valid {
			return board.ErrConflict
		}
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE point_entries SET reversed_at = ? WHERE id = ? AND reversed_at IS NULL`,
			formatTime(now), awardEntryID.String,
		)
		if err != nil {
			return err
		}
		if err := requireChanged(result); err != nil {
			return board.ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE users SET points = CASE WHEN points >= ? THEN points - ? ELSE 0 END
			WHERE username = ?`, points, points, username,
		); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `
        UPDATE tasks SET lifecycle = ?, scheduled_at = ?, claimed_by = ?, claimed_at = ?,
			points_snapshot = ?, points_snapshot_by = ?, points_snapshot_at = ?,
			unclaimed_at = ?, last_completed_at = ?, updated_at = ?, version = version + 1
        WHERE id = ? AND version = ?`,
		previousLifecycle, nullStringValue(previousScheduled), nullStringValue(previousClaimedBy),
		nullStringValue(previousClaimedAt), nullInt64Value(previousSnapshot),
		nullStringValue(previousSnapshotBy), nullStringValue(previousSnapshotAt),
		nullStringValue(previousUnclaimedAt), nullStringValue(previousLastCompleted),
		formatTime(now), task.ID, task.Version,
	)
	if err != nil {
		return err
	}
	if err := requireChanged(result); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE task_occurrences SET undone_at = ? WHERE id = ?`, formatTime(now), occurrenceID)
	return err
}

func listTasks(ctx context.Context, query queryer) ([]*board.Task, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT t.id, t.title, t.description, t.lifecycle, t.scheduled_at, t.deadline, t.lead_days,
            t.recurrence_kind, t.recurrence_days, t.recurrence_paused,
			t.time_critical, t.remedy_for, t.created_by, t.claimed_by, t.claimed_at, t.block_note, t.last_completed_at,
			t.points_snapshot, t.points_snapshot_by, t.points_snapshot_at, t.unclaimed_at,
			t.created_at, t.updated_at, t.version,
            EXISTS (
                SELECT 1 FROM task_occurrences o
                WHERE o.task_id = t.id AND o.outcome = 'completed'
                  AND o.undone_at IS NULL AND o.result_version = t.version
			),
			(SELECT p.username FROM point_entries p
			 WHERE p.task_id = t.id AND p.occurred_at = t.last_completed_at
			   AND p.reversed_at IS NULL ORDER BY p.id DESC LIMIT 1),
			(SELECT p.points FROM point_entries p
			 WHERE p.task_id = t.id AND p.occurred_at = t.last_completed_at
			   AND p.reversed_at IS NULL ORDER BY p.id DESC LIMIT 1),
			(SELECT p.occurred_at FROM point_entries p
			 WHERE p.task_id = t.id AND p.occurred_at = t.last_completed_at
			   AND p.reversed_at IS NULL ORDER BY p.id DESC LIMIT 1)
        FROM tasks t
        ORDER BY t.created_at, t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := make([]*board.Task, 0)
	byID := make(map[string]*board.Task)
	for rows.Next() {
		task := &board.Task{}
		var lifecycle string
		var scheduled, deadline, remedyFor, createdBy, claimedBy, claimedAt, lastCompleted sql.NullString
		var snapshotBy, snapshotAt, unclaimedAt, awardedTo, awardedAt sql.NullString
		var snapshotPoints, awardedPoints sql.NullInt64
		var recurrencePaused, timeCritical int
		var createdAt, updatedAt string
		var canUndo int
		if err := rows.Scan(
			&task.ID, &task.Title, &task.Description, &lifecycle, &scheduled, &deadline, &task.LeadDays,
			&task.Recurrence.Kind, &task.Recurrence.Days, &recurrencePaused,
			&timeCritical, &remedyFor, &createdBy,
			&claimedBy, &claimedAt, &task.BlockNote, &lastCompleted,
			&snapshotPoints, &snapshotBy, &snapshotAt, &unclaimedAt,
			&createdAt, &updatedAt, &task.Version, &canUndo,
			&awardedTo, &awardedPoints, &awardedAt,
		); err != nil {
			return nil, err
		}
		task.Lifecycle = board.Lifecycle(lifecycle)
		task.Recurrence.Paused = recurrencePaused == 1
		task.TimeCritical = timeCritical == 1
		task.RemedyFor = parseNullableString(remedyFor)
		task.CreatedBy = parseNullableString(createdBy)
		task.ScheduledAt, err = parseNullableTime(scheduled)
		if err != nil {
			return nil, err
		}
		task.Deadline, err = parseNullableTime(deadline)
		if err != nil {
			return nil, err
		}
		task.ClaimedBy = parseNullableString(claimedBy)
		task.ClaimedAt, err = parseNullableTime(claimedAt)
		if err != nil {
			return nil, err
		}
		if snapshotPoints.Valid {
			value := int(snapshotPoints.Int64)
			task.PointsSnapshot = &value
		}
		task.SnapshotBy = parseNullableString(snapshotBy)
		task.SnapshotAt, err = parseNullableTime(snapshotAt)
		if err != nil {
			return nil, err
		}
		task.UnclaimedAt, err = parseNullableTime(unclaimedAt)
		if err != nil {
			return nil, err
		}
		if awardedTo.Valid && awardedPoints.Valid && awardedAt.Valid {
			at, err := parseTime(awardedAt.String)
			if err != nil {
				return nil, err
			}
			task.Awarded = &board.PointAward{To: awardedTo.String, Points: int(awardedPoints.Int64), At: at}
			task.SnapshotAwarded = awardedPoints.Int64 > 0
		}
		task.LastCompletedAt, err = parseNullableTime(lastCompleted)
		if err != nil {
			return nil, err
		}
		task.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		task.UpdatedAt, err = parseTime(updatedAt)
		if err != nil {
			return nil, err
		}
		task.CanUndo = canUndo == 1
		task.Dependencies = []string{}
		task.Recurrence.Weekdays = []int{}
		tasks = append(tasks, task)
		byID[task.ID] = task
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	dependencyRows, err := query.QueryContext(ctx, `SELECT task_id, depends_on_id FROM task_dependencies ORDER BY task_id, depends_on_id`)
	if err != nil {
		return nil, err
	}
	for dependencyRows.Next() {
		var taskID, dependencyID string
		if err := dependencyRows.Scan(&taskID, &dependencyID); err != nil {
			return nil, err
		}
		if task := byID[taskID]; task != nil {
			task.Dependencies = append(task.Dependencies, dependencyID)
		}
	}
	if err := dependencyRows.Err(); err != nil {
		dependencyRows.Close()
		return nil, err
	}
	if err := dependencyRows.Close(); err != nil {
		return nil, err
	}
	weekdayRows, err := query.QueryContext(ctx, `
		SELECT task_id, weekday FROM task_recurrence_weekdays ORDER BY task_id, weekday`)
	if err != nil {
		return nil, err
	}
	defer weekdayRows.Close()
	for weekdayRows.Next() {
		var taskID string
		var weekday int
		if err := weekdayRows.Scan(&taskID, &weekday); err != nil {
			return nil, err
		}
		if task := byID[taskID]; task != nil {
			task.Recurrence.Weekdays = append(task.Recurrence.Weekdays, weekday)
		}
	}
	return tasks, weekdayRows.Err()
}

func ensureDependenciesExist(ctx context.Context, tx *sql.Tx, taskID string, dependencies []string) error {
	for _, dependencyID := range dependencies {
		if dependencyID == taskID {
			return errors.New("task cannot depend on itself")
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id = ?)`, dependencyID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("dependency %s does not exist", dependencyID)
		}
	}
	return nil
}

func listWIPLimits(ctx context.Context, query queryer) (board.WIPLimits, error) {
	rows, err := query.QueryContext(ctx, `SELECT state, limit_count FROM wip_limits ORDER BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	limits := make(board.WIPLimits)
	for rows.Next() {
		var state string
		var value sql.NullInt64
		if err := rows.Scan(&state, &value); err != nil {
			return nil, err
		}
		if value.Valid {
			limit := int(value.Int64)
			limits[board.EffectiveState(state)] = &limit
		} else {
			limits[board.EffectiveState(state)] = nil
		}
	}
	return limits, rows.Err()
}

func enforceWIP(ctx context.Context, tx *sql.Tx, tasks []*board.Task, state board.EffectiveState, excludeID string, now time.Time) error {
	limits, err := listWIPLimits(ctx, tx)
	if err != nil {
		return err
	}
	limit := limits[state]
	if limit == nil {
		return nil
	}
	board.DeriveStates(tasks, now)
	count := 0
	for _, task := range tasks {
		if task.ID != excludeID && task.EffectiveState == state {
			count++
		}
	}
	if count+1 > *limit {
		return &board.WIPLimitError{State: state, Limit: *limit}
	}
	return nil
}

func replaceDependencies(ctx context.Context, tx *sql.Tx, taskID string, dependencies []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_dependencies WHERE task_id = ?`, taskID); err != nil {
		return err
	}
	for _, dependencyID := range dependencies {
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_dependencies(task_id, depends_on_id) VALUES (?, ?)`, taskID, dependencyID); err != nil {
			return err
		}
	}
	return nil
}

func cleanupRemovedRemedies(ctx context.Context, tx *sql.Tx, parent *board.Task, newDependencies, cleanupIDs []string) error {
	if len(cleanupIDs) == 0 {
		return nil
	}
	previous := make(map[string]struct{}, len(parent.Dependencies))
	for _, dependencyID := range parent.Dependencies {
		previous[dependencyID] = struct{}{}
	}
	current := make(map[string]struct{}, len(newDependencies))
	for _, dependencyID := range newDependencies {
		current[dependencyID] = struct{}{}
	}
	for _, remedyID := range cleanupIDs {
		if _, existed := previous[remedyID]; !existed {
			return fmt.Errorf("task %s was not a dependency", remedyID)
		}
		if _, retained := current[remedyID]; retained {
			return fmt.Errorf("cannot clean up remedy %s while retaining its dependency", remedyID)
		}
		var remedyFor sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT remedy_for FROM tasks WHERE id = ?`, remedyID).Scan(&remedyFor)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("remedy task %s does not exist", remedyID)
		}
		if err != nil {
			return err
		}
		if !remedyFor.Valid || remedyFor.String != parent.ID {
			return fmt.Errorf("task %s is not a remedy for this task", remedyID)
		}
		var remainingDependents int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM task_dependencies WHERE depends_on_id = ?`, remedyID,
		).Scan(&remainingDependents); err != nil {
			return err
		}
		if remainingDependents > 0 {
			return fmt.Errorf("remedy %s is still used by another task", remedyID)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, remedyID); err != nil {
			return err
		}
	}
	return nil
}

func replaceRecurrenceWeekdays(ctx context.Context, tx *sql.Tx, taskID string, weekdays []int) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_recurrence_weekdays WHERE task_id = ?`, taskID); err != nil {
		return err
	}
	for _, weekday := range weekdays {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO task_recurrence_weekdays(task_id, weekday) VALUES (?, ?)`, taskID, weekday); err != nil {
			return err
		}
	}
	return nil
}

func wouldCreateCycle(tasks []*board.Task, taskID string, dependencies []string) bool {
	graph := make(map[string][]string, len(tasks))
	for _, task := range tasks {
		graph[task.ID] = append([]string(nil), task.Dependencies...)
	}
	graph[taskID] = append([]string(nil), dependencies...)
	visiting := make(map[string]bool)
	visited := make(map[string]bool)
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			return true
		}
		if visited[id] {
			return false
		}
		visiting[id] = true
		for _, dependencyID := range graph[id] {
			if visit(dependencyID) {
				return true
			}
		}
		visiting[id] = false
		visited[id] = true
		return false
	}
	return visit(taskID)
}

func findTask(tasks []*board.Task, id string) (*board.Task, error) {
	for _, task := range tasks {
		if task.ID == id {
			return task, nil
		}
	}
	return nil, board.ErrNotFound
}

func requireChanged(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return board.ErrConflict
	}
	return nil
}

func newID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", encoded[0:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:32]), nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

func parseNullableTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func parseNullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	copy := value.String
	return &copy
}

func timeValue(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func stringValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullStringValue(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func nullInt64Value(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func intValue(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
