package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"kanban-go/internal/board"
	"kanban-go/internal/legacy"
)

func loadImportFixture(t *testing.T) *legacy.Bundle {
	t.Helper()
	bundle, err := legacy.Load(filepath.Join("..", "legacy", "testdata", "complete"))
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestImportLegacyPreservesDataAndExactUndo(t *testing.T) {
	store, ctx, _ := openTestStore(t)
	bundle := loadImportFixture(t)
	report, err := store.ImportLegacy(ctx, bundle, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Tasks != 8 || report.Users != 2 || report.CompletionEntries != 1 {
		t.Fatalf("import report = %#v", report)
	}
	if report.UndoImported != 1 || report.UndoSkipped != 0 {
		t.Fatalf("undo report = %#v", report)
	}

	user, err := store.User(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if user.PasswordHash != bundle.Users[0].PasswordHash {
		t.Fatalf("imported user = %#v", user)
	}
	blocked, err := store.Get(ctx, "task-blocked")
	if err != nil {
		t.Fatal(err)
	}
	if blocked.EffectiveState != board.StateBlocked || blocked.BlockNote == "" || blocked.Deadline == nil {
		t.Fatalf("imported blocked task = %#v", blocked)
	}
	suspended, err := store.Get(ctx, "task-suspended")
	if err != nil {
		t.Fatal(err)
	}
	if suspended.Lifecycle != board.LifecycleReady || suspended.EffectiveState != board.StateSuspended {
		t.Fatalf("imported suspended task lifecycle/state = %s/%s", suspended.Lifecycle, suspended.EffectiveState)
	}
	done, err := store.Get(ctx, "task-done")
	if err != nil {
		t.Fatal(err)
	}
	if !done.CanUndo || done.ClaimedBy == nil || *done.ClaimedBy != "alice" {
		t.Fatalf("imported completion metadata = undo %v, owner %v", done.CanUndo, done.ClaimedBy)
	}
	done, err = store.Action(ctx, done.ID, "undo", "alice", board.ActionInput{Version: done.Version})
	if err != nil {
		t.Fatal(err)
	}
	if done.EffectiveState != board.StateInProgress || done.ClaimedBy == nil || *done.ClaimedBy != "alice" {
		t.Fatalf("restored task = state %s, owner %v", done.EffectiveState, done.ClaimedBy)
	}
	history, err := store.CompletionHistory(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Fatalf("history after imported undo = %#v", history)
	}
	limits, err := store.WIPLimits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if limits[board.StateReady] == nil || *limits[board.StateReady] != 20 {
		t.Fatalf("ready WIP limit = %v", limits[board.StateReady])
	}
}

func TestImportLegacyRequiresReplaceAndRollsBackFailure(t *testing.T) {
	store, ctx, _ := openTestStore(t)
	existing, err := store.Create(ctx, board.TaskInput{Title: "Keep me"})
	if err != nil {
		t.Fatal(err)
	}
	bundle := loadImportFixture(t)
	if _, err := store.ImportLegacy(ctx, bundle, false); !errors.Is(err, ErrDestinationNotEmpty) {
		t.Fatalf("non-replace import error = %v", err)
	}
	if _, err := store.Get(ctx, existing.ID); err != nil {
		t.Fatalf("existing task after rejected import: %v", err)
	}

	broken := *bundle
	broken.Users = append([]legacy.User(nil), bundle.Users...)
	broken.Users = append(broken.Users, bundle.Users[0])
	if _, err := store.ImportLegacy(ctx, &broken, true); err == nil {
		t.Fatal("expected duplicate user import to fail")
	}
	if _, err := store.Get(ctx, existing.ID); err != nil {
		t.Fatalf("existing task after rolled-back import: %v", err)
	}
}

func TestBackupToCreatesIndependentConsistentDatabase(t *testing.T) {
	store, ctx, _ := openTestStore(t)
	first, err := store.Create(ctx, board.TaskInput{Title: "Backed up"})
	if err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "nested", "backup.db")
	if err := store.BackupTo(ctx, backupPath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, board.TaskInput{Title: "After backup"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %o", info.Mode().Perm())
	}
	backup, err := Open(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	tasks, err := backup.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != first.ID {
		t.Fatalf("backup tasks = %#v", tasks)
	}
	if err := store.BackupTo(ctx, backupPath); err == nil {
		t.Fatal("expected existing backup destination to be rejected")
	}
}
