package board

import (
	"testing"
	"time"
)

func TestPriorityCombinesDueDateAndDependents(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	dueSoon := now.AddDate(0, 0, 1)
	tasks := []*Task{
		{ID: "root", Lifecycle: LifecycleReady, CreatedAt: now},
		{ID: "dependent", Lifecycle: LifecycleReady, Dependencies: []string{"root"}, CreatedAt: now},
		{ID: "urgent", Lifecycle: LifecycleReady, ScheduledAt: &dueSoon, CreatedAt: now},
	}

	ComputePriorities(tasks, now)
	if tasks[0].Priority <= tasks[1].Priority {
		t.Fatalf("prerequisite priority %d <= dependent priority %d", tasks[0].Priority, tasks[1].Priority)
	}
	if tasks[2].Priority <= 1 || tasks[2].Urgency <= 0 {
		t.Fatalf("urgent task priority/urgency = %d/%f", tasks[2].Priority, tasks[2].Urgency)
	}
}

func TestDoneDependentsDoNotIncreasePriority(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	tasks := []*Task{
		{ID: "root", Lifecycle: LifecycleReady, CreatedAt: now},
		{ID: "done", Lifecycle: LifecycleDone, Dependencies: []string{"root"}, CreatedAt: now},
	}
	ComputePriorities(tasks, now)
	if tasks[0].Priority != 1 || tasks[0].Importance != 0 {
		t.Fatalf("root priority/importance = %d/%f", tasks[0].Priority, tasks[0].Importance)
	}
}
