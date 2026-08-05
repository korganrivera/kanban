package store

import (
	"testing"

	"kanban-go/internal/board"
)

func TestCompletionHistoryUndoAndDeletion(t *testing.T) {
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
	if first.EffectiveState != board.StateDone || first.ClaimedBy == nil || *first.ClaimedBy != "alice" {
		t.Fatalf("completed task = state %s, owner %v", first.EffectiveState, first.ClaimedBy)
	}
	entries, err := database.CompletionHistory(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("completion history = %#v", entries)
	}

	first, err = database.Action(ctx, first.ID, "undo", "alice", board.ActionInput{Version: first.Version})
	if err != nil {
		t.Fatal(err)
	}
	if first.EffectiveState != board.StateInProgress || first.ClaimedBy == nil || *first.ClaimedBy != "alice" {
		t.Fatalf("restored task = state %s, owner %v", first.EffectiveState, first.ClaimedBy)
	}
	entries, err = database.CompletionHistory(ctx, "alice")
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
