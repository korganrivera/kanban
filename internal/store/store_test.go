package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"kanban-go/internal/board"
)

func openTestStore(t *testing.T) (*Store, context.Context, time.Time) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "kanban.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	return store, context.Background(), now
}

func TestScheduleAndDependenciesDeriveColumns(t *testing.T) {
	store, ctx, now := openTestStore(t)
	future := now.AddDate(0, 0, 7)
	waiting, err := store.Create(ctx, board.TaskInput{Title: "Future task", ScheduledAt: &future})
	if err != nil {
		t.Fatal(err)
	}
	if waiting.EffectiveState != board.StateWaiting {
		t.Fatalf("future task state = %s", waiting.EffectiveState)
	}

	prerequisite, err := store.Create(ctx, board.TaskInput{Title: "Prerequisite"})
	if err != nil {
		t.Fatal(err)
	}
	dependent, err := store.Create(ctx, board.TaskInput{Title: "Dependent", Dependencies: []string{prerequisite.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if dependent.Lifecycle != board.LifecycleReady || dependent.EffectiveState != board.StateSuspended {
		t.Fatalf("dependent lifecycle/state = %s/%s", dependent.Lifecycle, dependent.EffectiveState)
	}

	dependent, err = store.Update(ctx, dependent.ID, board.TaskInput{
		Title: dependent.Title, Description: dependent.Description,
		Recurrence: dependent.Recurrence, Dependencies: []string{}, Version: dependent.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dependent.EffectiveState != board.StateReady {
		t.Fatalf("released dependent state = %s", dependent.EffectiveState)
	}
}

func TestDependencyCyclesAreRejected(t *testing.T) {
	store, ctx, _ := openTestStore(t)
	first, err := store.Create(ctx, board.TaskInput{Title: "First"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(ctx, board.TaskInput{Title: "Second", Dependencies: []string{first.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(ctx, first.ID, board.TaskInput{
		Title: first.Title, Recurrence: first.Recurrence,
		Dependencies: []string{second.ID}, Version: first.Version,
	})
	if err == nil {
		t.Fatal("expected dependency cycle to be rejected")
	}
}

func TestCompletionAndUndoAreTransactional(t *testing.T) {
	store, ctx, _ := openTestStore(t)
	task, err := store.Create(ctx, board.TaskInput{Title: "Complete me"})
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.Action(ctx, task.ID, "claim", "korgan", board.ActionInput{Version: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	if task.EffectiveState != board.StateInProgress {
		t.Fatalf("claimed state = %s", task.EffectiveState)
	}
	task, err = store.Action(ctx, task.ID, "complete", "korgan", board.ActionInput{Version: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	if task.EffectiveState != board.StateDone || !task.CanUndo {
		t.Fatalf("completed state/undo = %s/%v", task.EffectiveState, task.CanUndo)
	}
	if task.ClaimedBy == nil || *task.ClaimedBy != "korgan" {
		t.Fatalf("completed owner = %v", task.ClaimedBy)
	}
	task, err = store.Action(ctx, task.ID, "undo", "korgan", board.ActionInput{Version: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	if task.EffectiveState != board.StateInProgress || task.CanUndo {
		t.Fatalf("restored state/undo = %s/%v", task.EffectiveState, task.CanUndo)
	}
	if task.ClaimedBy == nil || *task.ClaimedBy != "korgan" {
		t.Fatalf("restored owner = %v", task.ClaimedBy)
	}
}

func TestSchedulingClaimedTaskForFutureReleasesClaim(t *testing.T) {
	store, ctx, now := openTestStore(t)
	task, err := store.Create(ctx, board.TaskInput{Title: "Reschedule me"})
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.Action(ctx, task.ID, "claim", "korgan", board.ActionInput{Version: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	future := now.AddDate(0, 0, 7)
	task, err = store.Update(ctx, task.ID, board.TaskInput{
		Title: task.Title, Description: task.Description, ScheduledAt: &future,
		LeadDays: task.LeadDays, Recurrence: task.Recurrence,
		Dependencies: task.Dependencies, Version: task.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.EffectiveState != board.StateWaiting {
		t.Fatalf("rescheduled state = %s", task.EffectiveState)
	}
	if task.ClaimedBy != nil || task.Lifecycle != board.LifecycleReady {
		t.Fatalf("rescheduled claim/lifecycle = %v/%s", task.ClaimedBy, task.Lifecycle)
	}
}

func TestRecurringCompletionAdvancesAndCanUndo(t *testing.T) {
	store, ctx, now := openTestStore(t)
	due := now.Add(-time.Hour)
	task, err := store.Create(ctx, board.TaskInput{
		Title: "Plant potatoes", ScheduledAt: &due,
		Recurrence: board.Recurrence{Kind: "anchored", Days: 365},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.Action(ctx, task.ID, "claim", "korgan", board.ActionInput{Version: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.Action(ctx, task.ID, "complete", "korgan", board.ActionInput{Version: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	want := due.AddDate(0, 0, 365)
	if task.ScheduledAt == nil || !task.ScheduledAt.Equal(want) {
		t.Fatalf("next schedule = %v, want %v", task.ScheduledAt, want)
	}
	if task.EffectiveState != board.StateWaiting || !task.CanUndo {
		t.Fatalf("recurring state/undo = %s/%v", task.EffectiveState, task.CanUndo)
	}
	task, err = store.Action(ctx, task.ID, "undo", "korgan", board.ActionInput{Version: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	if task.ScheduledAt == nil || !task.ScheduledAt.Equal(due) {
		t.Fatalf("restored schedule = %v, want %v", task.ScheduledAt, due)
	}
}

func TestAnchoredWeekdaysPersistAndAdvance(t *testing.T) {
	store, ctx, now := openTestStore(t)
	due := time.Date(2026, time.August, 5, 8, 30, 0, 0, time.UTC)
	task, err := store.Create(ctx, board.TaskInput{
		Title: "Weekday recurrence", ScheduledAt: &due,
		Recurrence: board.Recurrence{Kind: "anchored", Weekdays: []int{1, 3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Recurrence.Weekdays) != 2 || task.Recurrence.Weekdays[0] != 1 || task.Recurrence.Weekdays[1] != 3 {
		t.Fatalf("stored weekdays = %v", task.Recurrence.Weekdays)
	}
	task, err = store.Action(ctx, task.ID, "claim", "korgan", board.ActionInput{Version: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.Action(ctx, task.ID, "complete", "korgan", board.ActionInput{Version: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, time.August, 10, 8, 30, 0, 0, time.UTC)
	if task.ScheduledAt == nil || !task.ScheduledAt.Equal(want) {
		t.Fatalf("next weekday schedule = %v, want %v (now %v)", task.ScheduledAt, want, now)
	}
}

func TestDeletingPrerequisiteRequiresConfirmation(t *testing.T) {
	store, ctx, _ := openTestStore(t)
	prerequisite, err := store.Create(ctx, board.TaskInput{Title: "Prerequisite"})
	if err != nil {
		t.Fatal(err)
	}
	dependent, err := store.Create(ctx, board.TaskInput{
		Title: "Dependent", Dependencies: []string{prerequisite.ID},
	})
	if err != nil {
		t.Fatal(err)
	}

	dependents, err := store.Delete(ctx, prerequisite.ID, board.DeleteInput{Version: prerequisite.Version})
	if !errors.Is(err, board.ErrHasDependents) {
		t.Fatalf("delete error = %v", err)
	}
	if len(dependents) != 1 || dependents[0].ID != dependent.ID {
		t.Fatalf("delete dependents = %#v", dependents)
	}

	dependents, err = store.Delete(ctx, prerequisite.ID, board.DeleteInput{
		Version: prerequisite.Version, Force: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dependents) != 1 {
		t.Fatalf("adjusted dependents = %#v", dependents)
	}
	if _, err := store.Get(ctx, prerequisite.ID); !errors.Is(err, board.ErrNotFound) {
		t.Fatalf("deleted task lookup error = %v", err)
	}
	dependent, err = store.Get(ctx, dependent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dependent.Dependencies) != 0 || dependent.EffectiveState != board.StateReady {
		t.Fatalf("adjusted dependent = dependencies %v, state %s", dependent.Dependencies, dependent.EffectiveState)
	}
}

func TestStaleVersionIsRejected(t *testing.T) {
	store, ctx, _ := openTestStore(t)
	task, err := store.Create(ctx, board.TaskInput{Title: "Versioned"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Action(ctx, task.ID, "claim", "korgan", board.ActionInput{Version: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Action(ctx, task.ID, "claim", "korgan", board.ActionInput{Version: task.Version})
	if !errors.Is(err, board.ErrConflict) {
		t.Fatalf("stale action error = %v", err)
	}
}

func TestWIPLimitPreventsAdditionalClaim(t *testing.T) {
	store, ctx, _ := openTestStore(t)
	limit := 1
	limits, err := store.UpdateWIPLimits(ctx, board.WIPLimits{board.StateInProgress: &limit})
	if err != nil {
		t.Fatal(err)
	}
	if limits[board.StateInProgress] == nil || *limits[board.StateInProgress] != 1 {
		t.Fatalf("in-progress limit = %v", limits[board.StateInProgress])
	}

	first, err := store.Create(ctx, board.TaskInput{Title: "First task"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(ctx, board.TaskInput{Title: "Second task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Action(ctx, first.ID, "claim", "korgan", board.ActionInput{Version: first.Version}); err != nil {
		t.Fatal(err)
	}
	_, err = store.Action(ctx, second.ID, "claim", "korgan", board.ActionInput{Version: second.Version})
	var wipError *board.WIPLimitError
	if !errors.As(err, &wipError) || wipError.State != board.StateInProgress || wipError.Limit != 1 {
		t.Fatalf("second claim error = %v", err)
	}
}

func TestRemedySuspendsBlockedTask(t *testing.T) {
	store, ctx, _ := openTestStore(t)
	blocked, err := store.Create(ctx, board.TaskInput{
		Title: "Blocked parent", TimeCritical: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	blocked, err = store.Action(ctx, blocked.ID, "block", "korgan", board.ActionInput{
		Version: blocked.Version, Note: "Needs a concrete next action",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.CreateRemedy(ctx, blocked.ID, board.RemedyInput{
		Title: "Resolve the blocker", Version: blocked.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.BlockedTask.EffectiveState != board.StateSuspended || result.BlockedTask.BlockNote != "" {
		t.Fatalf("parent state/note = %s/%q", result.BlockedTask.EffectiveState, result.BlockedTask.BlockNote)
	}
	if len(result.BlockedTask.Dependencies) != 1 || result.BlockedTask.Dependencies[0] != result.RemedyTask.ID {
		t.Fatalf("parent dependencies = %v", result.BlockedTask.Dependencies)
	}
	if result.RemedyTask.RemedyFor == nil || *result.RemedyTask.RemedyFor != blocked.ID {
		t.Fatalf("remedy parent = %v", result.RemedyTask.RemedyFor)
	}
	if result.RemedyTask.EffectiveState != board.StateReady {
		t.Fatalf("remedy state = %s", result.RemedyTask.EffectiveState)
	}
	if !result.BlockedTask.TimeCritical {
		t.Fatal("time-critical metadata was not persisted on parent")
	}
}
