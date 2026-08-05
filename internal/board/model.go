package board

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Lifecycle string

const (
	LifecycleReady      Lifecycle = "Ready"
	LifecycleInProgress Lifecycle = "InProgress"
	LifecycleBlocked    Lifecycle = "Blocked"
	LifecycleDone       Lifecycle = "Done"
)

type EffectiveState string

const (
	StateWaiting    EffectiveState = "Waiting"
	StateReady      EffectiveState = "Ready"
	StateInProgress EffectiveState = "InProgress"
	StateBlocked    EffectiveState = "Blocked"
	StateSuspended  EffectiveState = "Suspended"
	StateDone       EffectiveState = "Done"
)

type Recurrence struct {
	Kind     string `json:"kind"`
	Days     int    `json:"days"`
	Weekdays []int  `json:"weekdays"`
	Paused   bool   `json:"paused"`
}

type Task struct {
	ID              string         `json:"id"`
	Title           string         `json:"title"`
	Description     string         `json:"description"`
	Lifecycle       Lifecycle      `json:"lifecycle"`
	EffectiveState  EffectiveState `json:"effectiveState"`
	Dependencies    []string       `json:"dependencies"`
	ScheduledAt     *time.Time     `json:"scheduledAt"`
	ReadyAt         *time.Time     `json:"readyAt"`
	LeadDays        int            `json:"leadDays"`
	Recurrence      Recurrence     `json:"recurrence"`
	TimeCritical    bool           `json:"timeCritical"`
	RemedyFor       *string        `json:"remedyFor"`
	CreatedBy       *string        `json:"createdBy"`
	ClaimedBy       *string        `json:"claimedBy"`
	ClaimedAt       *time.Time     `json:"claimedAt"`
	BlockNote       string         `json:"blockNote"`
	LastCompletedAt *time.Time     `json:"lastCompletedAt"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
	Version         int64          `json:"version"`
	CanUndo         bool           `json:"canUndo"`
	Priority        int            `json:"priority"`
	Importance      float64        `json:"importance"`
	Urgency         float64        `json:"urgency"`
	Deadlock        bool           `json:"deadlock"`
}

type TaskInput struct {
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	BlockNote    string     `json:"blockNote"`
	TimeCritical bool       `json:"timeCritical"`
	ScheduledAt  *time.Time `json:"scheduledAt"`
	LeadDays     int        `json:"leadDays"`
	Recurrence   Recurrence `json:"recurrence"`
	Dependencies []string   `json:"dependencies"`
	Version      int64      `json:"version"`
	CreatedBy    *string    `json:"-"`
}

type ActionInput struct {
	Version int64  `json:"version"`
	Note    string `json:"note"`
}

type DeleteInput struct {
	Version int64 `json:"version"`
	Force   bool  `json:"force"`
}

type TaskReference struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type RemedyInput struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Version     int64  `json:"version"`
}

type RemedyResult struct {
	BlockedTask *Task `json:"blockedTask"`
	RemedyTask  *Task `json:"remedyTask"`
}

type WIPLimits map[EffectiveState]*int

type WIPLimitError struct {
	State EffectiveState
	Limit int
}

func (err *WIPLimitError) Error() string {
	return fmt.Sprintf("WIP limit exceeded for %s; limit: %d", err.State, err.Limit)
}

var (
	ErrNotFound      = errors.New("task not found")
	ErrConflict      = errors.New("task changed since it was loaded")
	ErrInvalidAction = errors.New("action is not valid for this task")
	ErrHasDependents = errors.New("other tasks depend on this task")
)

func (input *TaskInput) Normalize() error {
	input.Title = strings.TrimSpace(input.Title)
	input.Description = strings.TrimSpace(input.Description)
	input.BlockNote = strings.TrimSpace(input.BlockNote)
	if input.Title == "" {
		return errors.New("title is required")
	}
	if len(input.Title) > 200 {
		return errors.New("title must be 200 characters or fewer")
	}
	if len(input.Description) > 20000 {
		return errors.New("description must be 20000 characters or fewer")
	}
	if len(input.BlockNote) > 5000 {
		return errors.New("block note must be 5000 characters or fewer")
	}
	if input.LeadDays < 0 {
		return errors.New("lead days cannot be negative")
	}
	if input.Recurrence.Kind == "" {
		input.Recurrence.Kind = "none"
	}
	if input.Recurrence.Kind != "none" && input.Recurrence.Kind != "rolling" && input.Recurrence.Kind != "anchored" {
		return errors.New("recurrence kind must be none, rolling, or anchored")
	}
	if input.Recurrence.Kind == "none" {
		input.Recurrence.Days = 0
		input.Recurrence.Weekdays = nil
		input.Recurrence.Paused = false
	} else {
		weekdays := make([]int, 0, len(input.Recurrence.Weekdays))
		seenWeekdays := make(map[int]struct{}, len(input.Recurrence.Weekdays))
		for _, weekday := range input.Recurrence.Weekdays {
			if weekday < 0 || weekday > 6 {
				return errors.New("recurrence weekdays must be between 0 and 6")
			}
			if _, exists := seenWeekdays[weekday]; exists {
				continue
			}
			seenWeekdays[weekday] = struct{}{}
			weekdays = append(weekdays, weekday)
		}
		sort.Ints(weekdays)
		input.Recurrence.Weekdays = weekdays
		if input.Recurrence.Kind == "rolling" {
			input.Recurrence.Weekdays = nil
			if input.Recurrence.Days <= 0 {
				return errors.New("rolling recurrence days must be positive")
			}
		} else {
			if input.Recurrence.Days < 0 {
				return errors.New("anchored recurrence days cannot be negative")
			}
			if input.Recurrence.Days == 0 && len(input.Recurrence.Weekdays) == 0 {
				return errors.New("anchored recurrence requires an interval or weekdays")
			}
		}
	}
	seen := make(map[string]struct{}, len(input.Dependencies))
	cleaned := make([]string, 0, len(input.Dependencies))
	for _, dependencyID := range input.Dependencies {
		dependencyID = strings.TrimSpace(dependencyID)
		if dependencyID == "" {
			continue
		}
		if _, exists := seen[dependencyID]; exists {
			continue
		}
		seen[dependencyID] = struct{}{}
		cleaned = append(cleaned, dependencyID)
	}
	input.Dependencies = cleaned
	return nil
}

func (input *RemedyInput) Normalize(parentTitle string) error {
	input.Title = strings.TrimSpace(input.Title)
	input.Description = strings.TrimSpace(input.Description)
	if input.Title == "" {
		input.Title = "Remedy for " + parentTitle
	}
	if input.Description == "" {
		input.Description = "Remedy for: " + parentTitle
	}
	if len(input.Title) > 200 {
		return errors.New("title must be 200 characters or fewer")
	}
	if len(input.Description) > 20000 {
		return errors.New("description must be 20000 characters or fewer")
	}
	if input.Version < 1 {
		return errors.New("version is required")
	}
	return nil
}

func ValidState(value EffectiveState) bool {
	switch value {
	case StateWaiting, StateReady, StateInProgress, StateBlocked, StateSuspended, StateDone:
		return true
	default:
		return false
	}
}

func DeriveStates(tasks []*Task, now time.Time) {
	byID := make(map[string]*Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID] = task
	}
	for _, task := range tasks {
		task.ReadyAt = nil
		if task.ScheduledAt != nil {
			readyAt := task.ScheduledAt.AddDate(0, 0, -task.LeadDays)
			task.ReadyAt = &readyAt
		}
		task.EffectiveState = EffectiveStateFor(task, byID, now)
	}
}

func EffectiveStateFor(task *Task, byID map[string]*Task, now time.Time) EffectiveState {
	if task.Lifecycle == LifecycleDone {
		return StateDone
	}
	if task.Lifecycle == LifecycleBlocked {
		return StateBlocked
	}
	if task.Recurrence.Paused {
		return StateSuspended
	}
	if task.ScheduledAt != nil {
		readyAt := task.ScheduledAt.AddDate(0, 0, -task.LeadDays)
		if now.Before(readyAt) {
			return StateWaiting
		}
	}
	if hasUnresolvedDependency(task, byID) {
		return StateSuspended
	}
	if task.Lifecycle == LifecycleInProgress && task.ClaimedBy != nil && strings.TrimSpace(*task.ClaimedBy) != "" {
		return StateInProgress
	}
	return StateReady
}

func hasUnresolvedDependency(task *Task, byID map[string]*Task) bool {
	requiredAfter := task.CreatedAt
	if task.LastCompletedAt != nil {
		requiredAfter = *task.LastCompletedAt
	}
	for _, dependencyID := range task.Dependencies {
		dependency := byID[dependencyID]
		if dependency == nil {
			return true
		}
		if dependency.Lifecycle == LifecycleDone {
			continue
		}
		if dependency.Recurrence.Kind != "none" && dependency.LastCompletedAt != nil && !dependency.LastCompletedAt.Before(requiredAfter) {
			continue
		}
		return true
	}
	return false
}

func AdvanceSchedule(task *Task, completedAt time.Time) (*time.Time, error) {
	if task.Recurrence.Kind == "none" {
		return nil, nil
	}
	if task.Recurrence.Kind == "anchored" && len(task.Recurrence.Weekdays) > 0 {
		location := completedAt.Location()
		base := completedAt
		if task.ScheduledAt != nil {
			location = task.ScheduledAt.Location()
			base = completedAt.In(location)
			base = time.Date(
				base.Year(), base.Month(), base.Day(),
				task.ScheduledAt.Hour(), task.ScheduledAt.Minute(), task.ScheduledAt.Second(), task.ScheduledAt.Nanosecond(),
				location,
			)
		}
		allowed := make(map[time.Weekday]struct{}, len(task.Recurrence.Weekdays))
		for _, weekday := range task.Recurrence.Weekdays {
			if weekday < 0 || weekday > 6 {
				return nil, fmt.Errorf("invalid recurrence weekday: %d", weekday)
			}
			allowed[time.Weekday(weekday)] = struct{}{}
		}
		for offset := 1; offset <= 14; offset++ {
			candidate := base.AddDate(0, 0, offset)
			if _, exists := allowed[candidate.Weekday()]; exists {
				return &candidate, nil
			}
		}
		return nil, errors.New("could not find the next recurrence weekday")
	}
	if task.Recurrence.Days <= 0 {
		return nil, fmt.Errorf("invalid recurrence interval: %d", task.Recurrence.Days)
	}
	base := completedAt
	if task.Recurrence.Kind == "anchored" && task.ScheduledAt != nil {
		base = *task.ScheduledAt
	}
	next := base.AddDate(0, 0, task.Recurrence.Days)
	return &next, nil
}
