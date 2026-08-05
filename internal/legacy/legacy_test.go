package legacy

import (
	"strings"
	"testing"
)

func TestLoadCompleteFixture(t *testing.T) {
	bundle, err := Load("testdata/complete")
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Report.Tasks != 8 || bundle.Report.Users != 2 {
		t.Fatalf("task/user counts = %d/%d", bundle.Report.Tasks, bundle.Report.Users)
	}
	if bundle.Report.CompletionEntries != 1 || bundle.Report.UndoCandidates != 1 {
		t.Fatalf("history counts = %#v", bundle.Report)
	}
	byID := make(map[string]Task, len(bundle.Tasks))
	states := make(map[string]bool)
	for _, task := range bundle.Tasks {
		byID[task.ID] = task
		states[task.State] = true
	}
	for _, state := range []string{"Waiting", "Ready", "InProgress", "Blocked", "Suspended", "Done"} {
		if !states[state] {
			t.Errorf("fixture is missing %s state", state)
		}
	}
	if task := byID["task-blocked"]; task.BlockNote == "" || task.Deadline == nil || !task.TimeCritical {
		t.Fatalf("blocked metadata = %#v", task)
	}
	if task := byID["task-weekdays"]; task.Recurrence.Kind != "anchored" || len(task.Recurrence.Weekdays) != 2 {
		t.Fatalf("weekday recurrence = %#v", task.Recurrence)
	}
	if task := byID["task-done"]; task.CompletionUndo == nil || task.CompletionUndo.CompletionUser != "alice" {
		t.Fatalf("completion undo = %#v", task.CompletionUndo)
	}
	if got := bundle.Users[0].PasswordHash; !strings.HasPrefix(got, "$2b$10$") {
		t.Fatalf("password hash was not retained: %q", got)
	}
}

func TestValidateRejectsMissingDependenciesAndCycles(t *testing.T) {
	missing := &Bundle{
		Tasks:  []Task{{ID: "first", State: "Ready", Recurrence: Recurrence{Kind: "none"}, Dependencies: []string{"missing"}}},
		Limits: map[string]*int{},
	}
	if err := validate(missing); err == nil || !strings.Contains(err.Error(), "missing dependency") {
		t.Fatalf("missing dependency error = %v", err)
	}
	cycle := &Bundle{
		Tasks: []Task{
			{ID: "first", State: "Ready", Recurrence: Recurrence{Kind: "none"}, Dependencies: []string{"second"}},
			{ID: "second", State: "Ready", Recurrence: Recurrence{Kind: "none"}, Dependencies: []string{"first"}},
		},
		Limits: map[string]*int{},
	}
	if err := validate(cycle); err == nil || !strings.Contains(err.Error(), "dependency cycle") {
		t.Fatalf("cycle error = %v", err)
	}
}

func TestValidateRejectsDuplicateUsers(t *testing.T) {
	bundle := &Bundle{
		Users:  []User{{Username: "alice"}, {Username: "alice"}},
		Limits: map[string]*int{},
	}
	if err := validate(bundle); err == nil || !strings.Contains(err.Error(), "duplicate user") {
		t.Fatalf("duplicate user error = %v", err)
	}
}
