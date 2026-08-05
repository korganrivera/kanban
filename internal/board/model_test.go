package board

import (
	"testing"
	"time"
)

func TestDerivedStates(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	future := now.AddDate(0, 0, 7)
	owner := "korgan"
	tasks := []*Task{
		{ID: "waiting", Lifecycle: LifecycleReady, ScheduledAt: &future, CreatedAt: now},
		{ID: "ready", Lifecycle: LifecycleReady, CreatedAt: now},
		{ID: "doing", Lifecycle: LifecycleInProgress, ClaimedBy: &owner, CreatedAt: now},
		{ID: "blocked", Lifecycle: LifecycleBlocked, CreatedAt: now},
		{ID: "paused", Lifecycle: LifecycleReady, Recurrence: Recurrence{Kind: "anchored", Days: 30, Paused: true}, CreatedAt: now},
		{ID: "dependent", Lifecycle: LifecycleReady, Dependencies: []string{"ready"}, CreatedAt: now},
		{ID: "done", Lifecycle: LifecycleDone, CreatedAt: now},
	}

	DeriveStates(tasks, now)
	want := []EffectiveState{StateWaiting, StateReady, StateInProgress, StateBlocked, StateSuspended, StateSuspended, StateDone}
	for index, task := range tasks {
		if task.EffectiveState != want[index] {
			t.Fatalf("task %s state = %s, want %s", task.ID, task.EffectiveState, want[index])
		}
	}
}

func TestAnchoredAndRollingSchedules(t *testing.T) {
	due := time.Date(2026, time.April, 1, 8, 0, 0, 0, time.UTC)
	completed := time.Date(2026, time.April, 3, 10, 0, 0, 0, time.UTC)

	anchored, err := AdvanceSchedule(&Task{
		ScheduledAt: &due,
		Recurrence:  Recurrence{Kind: "anchored", Days: 365},
	}, completed)
	if err != nil {
		t.Fatal(err)
	}
	if want := due.AddDate(0, 0, 365); !anchored.Equal(want) {
		t.Fatalf("anchored schedule = %s, want %s", anchored, want)
	}

	rolling, err := AdvanceSchedule(&Task{
		ScheduledAt: &due,
		Recurrence:  Recurrence{Kind: "rolling", Days: 30},
	}, completed)
	if err != nil {
		t.Fatal(err)
	}
	if want := completed.AddDate(0, 0, 30); !rolling.Equal(want) {
		t.Fatalf("rolling schedule = %s, want %s", rolling, want)
	}
}

func TestAnchoredWeekdaySchedulePreservesDueTime(t *testing.T) {
	due := time.Date(2026, time.August, 5, 8, 30, 0, 0, time.UTC)
	completed := time.Date(2026, time.August, 5, 15, 0, 0, 0, time.UTC)
	next, err := AdvanceSchedule(&Task{
		ScheduledAt: &due,
		Recurrence:  Recurrence{Kind: "anchored", Weekdays: []int{1, 3}},
	}, completed)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, time.August, 10, 8, 30, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("weekday schedule = %s, want %s", next, want)
	}
}
