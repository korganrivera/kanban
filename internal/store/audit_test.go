package store

import (
	"strings"
	"testing"

	"kanban-go/internal/board"
)

func TestAuditChecksDatabaseAndDomainInvariants(t *testing.T) {
	store, ctx, _ := openTestStore(t)
	task, err := store.Create(ctx, board.TaskInput{Title: "Audited task"})
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.Action(ctx, task.ID, "claim", "alice", board.ActionInput{Version: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Action(ctx, task.ID, "complete", "alice", board.ActionInput{Version: task.Version}); err != nil {
		t.Fatal(err)
	}
	report, err := store.Audit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK || report.Counts["tasks"] != 1 || report.Counts["users"] != 1 {
		t.Fatalf("clean audit = %#v", report)
	}

	if _, err := store.db.ExecContext(ctx, `UPDATE completion_entries SET reversed_at = '2026-08-05T13:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	report, err = store.Audit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.OK || len(report.Findings) == 0 || !strings.Contains(report.Findings[0], "reversed history") {
		t.Fatalf("corrupt domain audit = %#v", report)
	}
}

func TestAuditDetectsDependencyCycle(t *testing.T) {
	store, ctx, _ := openTestStore(t)
	first, err := store.Create(ctx, board.TaskInput{Title: "First"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(ctx, board.TaskInput{Title: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO task_dependencies(task_id, depends_on_id) VALUES (?, ?), (?, ?)`,
		first.ID, second.ID, second.ID, first.ID,
	); err != nil {
		t.Fatal(err)
	}
	report, err := store.Audit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("dependency cycle passed audit")
	}
	found := false
	for _, finding := range report.Findings {
		found = found || strings.Contains(finding, "dependency cycle")
	}
	if !found {
		t.Fatalf("cycle findings = %#v", report.Findings)
	}
}
