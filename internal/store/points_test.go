package store

import (
	"testing"
	"time"

	"kanban-go/internal/board"
)

func TestClaimPrioritySnapshotRefreshRules(t *testing.T) {
	database, ctx, initialNow := openTestStore(t)
	now := initialNow
	database.now = func() time.Time { return now }
	due := now.Add(48 * time.Hour)
	task, err := database.Create(ctx, board.TaskInput{Title: "Snapshot task", ScheduledAt: &due, LeadDays: 3})
	if err != nil {
		t.Fatal(err)
	}
	task, err = database.Action(ctx, task.ID, "claim", "alice", board.ActionInput{Version: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	if task.PointsSnapshot == nil || task.SnapshotBy == nil || *task.SnapshotBy != "alice" || task.SnapshotAt == nil {
		t.Fatalf("initial snapshot = points %v, by %v, at %v", task.PointsSnapshot, task.SnapshotBy, task.SnapshotAt)
	}
	firstPoints := *task.PointsSnapshot
	firstSnapshotAt := *task.SnapshotAt

	task, err = database.Action(ctx, task.ID, "release", "alice", board.ActionInput{Version: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Create(ctx, board.TaskInput{Title: "New dependent", Dependencies: []string{task.ID}}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	task, err = database.Action(ctx, task.ID, "claim", "alice", board.ActionInput{Version: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	if *task.PointsSnapshot != firstPoints || !task.SnapshotAt.Equal(firstSnapshotAt) {
		t.Fatalf("short release refreshed snapshot to %d at %v", *task.PointsSnapshot, task.SnapshotAt)
	}

	task, err = database.Action(ctx, task.ID, "release", "alice", board.ActionInput{Version: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(25 * time.Hour)
	task, err = database.Action(ctx, task.ID, "claim", "alice", board.ActionInput{Version: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	if task.SnapshotAt == nil || !task.SnapshotAt.Equal(now) {
		t.Fatalf("long release snapshot time = %v, want %v", task.SnapshotAt, now)
	}

	task, err = database.Action(ctx, task.ID, "release", "alice", board.ActionInput{Version: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	task, err = database.Action(ctx, task.ID, "claim", "bob", board.ActionInput{Version: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	if task.SnapshotBy == nil || *task.SnapshotBy != "bob" || task.SnapshotAt == nil || !task.SnapshotAt.Equal(now) {
		t.Fatalf("different claimant snapshot = by %v, at %v", task.SnapshotBy, task.SnapshotAt)
	}

	var snapshots int
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_point_snapshots WHERE task_id = ?`, task.ID).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if snapshots != 3 {
		t.Fatalf("snapshot history count = %d, want 3", snapshots)
	}
}

func TestCompletionPointsUndoAndDeletionHistory(t *testing.T) {
	database, ctx, _ := openTestStore(t)

	complete := func(title string) *board.Task {
		t.Helper()
		task, err := database.Create(ctx, board.TaskInput{Title: title})
		if err != nil {
			t.Fatal(err)
		}
		task, err = database.Action(ctx, task.ID, "claim", "alice", board.ActionInput{Version: task.Version})
		if err != nil {
			t.Fatal(err)
		}
		task, err = database.Action(ctx, task.ID, "complete", "alice", board.ActionInput{Version: task.Version})
		if err != nil {
			t.Fatal(err)
		}
		return task
	}

	first := complete("First completion")
	second := complete("Second completion")
	if first.Awarded == nil || first.PointsSnapshot == nil || first.Awarded.Points != *first.PointsSnapshot {
		t.Fatalf("first award/snapshot = %#v/%v", first.Awarded, first.PointsSnapshot)
	}
	user, err := database.User(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	wantTotal := first.Awarded.Points + second.Awarded.Points
	if user.Points != wantTotal {
		t.Fatalf("points total = %d, want %d", user.Points, wantTotal)
	}

	first, err = database.Action(ctx, first.ID, "undo", "alice", board.ActionInput{Version: first.Version})
	if err != nil {
		t.Fatal(err)
	}
	user, err = database.User(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if user.Points != second.Awarded.Points {
		t.Fatalf("points after exact undo = %d, want %d", user.Points, second.Awarded.Points)
	}
	if first.EffectiveState != board.StateInProgress || first.PointsSnapshot == nil {
		t.Fatalf("restored task state/snapshot = %s/%v", first.EffectiveState, first.PointsSnapshot)
	}

	entries, err := database.CompletionHistory(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].TaskID != second.ID {
		t.Fatalf("history after undo = %#v", entries)
	}
	if _, err := database.Delete(ctx, second.ID, board.DeleteInput{Version: second.Version}); err != nil {
		t.Fatal(err)
	}
	entries, err = database.CompletionHistory(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].TaskTitle != "Second completion" {
		t.Fatalf("history after task deletion = %#v", entries)
	}
}
