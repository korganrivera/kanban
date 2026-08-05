package legacy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Bundle struct {
	Tasks  []Task
	Users  []User
	Limits map[string]*int
	Report Report
}

type Report struct {
	Tasks          int      `json:"tasks"`
	Users          int      `json:"users"`
	PointEntries   int      `json:"pointEntries"`
	PointSnapshots int      `json:"pointSnapshots"`
	UndoCandidates int      `json:"undoCandidates"`
	SyntheticUsers int      `json:"syntheticUsers"`
	Warnings       []string `json:"warnings"`
}

type Task struct {
	ID             string
	Title          string
	Description    string
	State          string
	Dependencies   []string
	ScheduledAt    *time.Time
	Deadline       *time.Time
	LeadDays       int
	Recurrence     Recurrence
	Picker         *string
	PickedAt       *time.Time
	UnclaimedAt    *time.Time
	BlockNote      string
	LastCompleted  *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CreatedBy      *string
	TimeCritical   bool
	RemedyFor      *string
	PointsSnapshot *int
	SnapshotBy     *string
	SnapshotAt     *time.Time
	Snapshots      []PointSnapshot
	CompletionUndo *CompletionUndo
}

type Recurrence struct {
	Kind     string
	Days     int
	Weekdays []int
	Paused   bool
}

type PointSnapshot struct {
	Points    int
	Username  string
	CreatedAt time.Time
}

type CompletionUndo struct {
	CompletionID string
	CompletedAt  time.Time
	Previous     UndoTask
	AwardUser    string
}

type UndoTask struct {
	State          string
	ScheduledAt    *time.Time
	Picker         *string
	PickedAt       *time.Time
	LastCompleted  *time.Time
	PointsSnapshot *int
	SnapshotBy     *string
	SnapshotAt     *time.Time
	UnclaimedAt    *time.Time
}

type User struct {
	Username          string
	PasswordHash      string
	Points            int
	CreatedAt         time.Time
	PasswordChangedAt *time.Time
	History           []PointEntry
}

type PointEntry struct {
	OccurredAt   time.Time
	Points       int
	Reason       string
	CompletionID string
	TaskID       string
	TaskTitle    string
}

type rawTask struct {
	ID             string             `json:"id"`
	Title          string             `json:"title"`
	Description    string             `json:"description"`
	State          string             `json:"state"`
	Dependencies   []string           `json:"dependencies"`
	ScheduledAt    *string            `json:"scheduledDueAt"`
	Deadline       *string            `json:"deadline"`
	LeadDays       int                `json:"leadTimeDays"`
	Recurrence     *rawRecurrence     `json:"recurrence"`
	Picker         *string            `json:"picker"`
	PickedAt       *string            `json:"picked_at"`
	UnclaimedAt    *string            `json:"unclaimed_at"`
	Meta           rawMeta            `json:"meta"`
	LastCompleted  *string            `json:"lastCompletedAt"`
	CreatedAt      string             `json:"created_at"`
	UpdatedAt      string             `json:"updated_at"`
	CreatedBy      *string            `json:"created_by"`
	TimeCritical   bool               `json:"timeCritical"`
	RemedyFor      *string            `json:"remedy_for"`
	PointsSnapshot *float64           `json:"points_snapshot"`
	SnapshotBy     *string            `json:"points_snapshot_created_by"`
	SnapshotAt     *string            `json:"points_snapshot_created_at"`
	PointsHistory  []rawSnapshot      `json:"points_history"`
	CompletionUndo *rawCompletionUndo `json:"completionUndo"`
}

type rawRecurrence struct {
	Kind     string `json:"type"`
	Days     int    `json:"intervalDays"`
	Weekdays []int  `json:"weekdays"`
	LeadDays int    `json:"leadTimeDays"`
	Paused   bool   `json:"paused"`
}

type rawMeta struct {
	BlockNote string `json:"block_note"`
}

type rawSnapshot struct {
	Timestamp string  `json:"ts"`
	Points    float64 `json:"snapshot"`
	Username  string  `json:"by"`
}

type rawCompletionUndo struct {
	CompletionID string          `json:"completionId"`
	CompletedAt  string          `json:"completedAt"`
	PreviousTask json.RawMessage `json:"previousTask"`
	Award        *rawAward       `json:"award"`
}

type rawAward struct {
	User         string `json:"user"`
	CompletionID string `json:"completionId"`
}

type rawUser struct {
	Username          string          `json:"username"`
	PasswordHash      string          `json:"password"`
	Points            int             `json:"points"`
	CreatedAt         string          `json:"created_at"`
	PasswordChangedAt *string         `json:"password_changed_at"`
	History           []rawPointEntry `json:"history"`
}

type rawPointEntry struct {
	Timestamp    string `json:"ts"`
	Points       int    `json:"points"`
	Reason       string `json:"reason"`
	CompletionID string `json:"completionId"`
}

func Load(sourceDir string) (*Bundle, error) {
	var rawTasks []rawTask
	if err := readJSON(filepath.Join(sourceDir, "tasks.json"), &rawTasks); err != nil {
		return nil, err
	}
	var rawUsers map[string]rawUser
	if err := readJSON(filepath.Join(sourceDir, "users.json"), &rawUsers); err != nil {
		return nil, err
	}
	var limits map[string]*int
	if err := readJSON(filepath.Join(sourceDir, "wip_limits.json"), &limits); err != nil {
		return nil, err
	}

	bundle := &Bundle{Limits: limits}
	for index, raw := range rawTasks {
		task, err := normalizeTask(raw)
		if err != nil {
			return nil, fmt.Errorf("task %d (%q): %w", index, raw.ID, err)
		}
		bundle.Tasks = append(bundle.Tasks, task)
		bundle.Report.PointSnapshots += len(task.Snapshots)
		if task.CompletionUndo != nil {
			bundle.Report.UndoCandidates++
		}
	}

	keys := make([]string, 0, len(rawUsers))
	for key := range rawUsers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		user, warnings, err := normalizeUser(key, rawUsers[key])
		if err != nil {
			return nil, err
		}
		bundle.Users = append(bundle.Users, user)
		bundle.Report.PointEntries += len(user.History)
		bundle.Report.Warnings = append(bundle.Report.Warnings, warnings...)
	}
	ensureReferencedUsers(bundle)
	if err := validate(bundle); err != nil {
		return nil, err
	}
	bundle.Report.Tasks = len(bundle.Tasks)
	bundle.Report.Users = len(bundle.Users)
	return bundle, nil
}

func normalizeTask(raw rawTask) (Task, error) {
	createdAt, err := requiredTime(raw.CreatedAt, "created_at")
	if err != nil {
		return Task{}, err
	}
	updatedAt, err := requiredTime(raw.UpdatedAt, "updated_at")
	if err != nil {
		return Task{}, err
	}
	task := Task{
		ID: strings.TrimSpace(raw.ID), Title: strings.TrimSpace(raw.Title), Description: raw.Description,
		State: raw.State, Dependencies: cleanStrings(raw.Dependencies), LeadDays: raw.LeadDays,
		Picker: cleanString(raw.Picker), BlockNote: strings.TrimSpace(raw.Meta.BlockNote),
		CreatedAt: createdAt, UpdatedAt: updatedAt, CreatedBy: cleanString(raw.CreatedBy),
		TimeCritical: raw.TimeCritical, RemedyFor: cleanString(raw.RemedyFor), SnapshotBy: cleanString(raw.SnapshotBy),
	}
	if task.ID == "" || task.Title == "" {
		return Task{}, errors.New("id and title are required")
	}
	if task.ScheduledAt, err = optionalTime(raw.ScheduledAt); err != nil {
		return Task{}, fmt.Errorf("scheduledDueAt: %w", err)
	}
	if task.Deadline, err = optionalTime(raw.Deadline); err != nil {
		return Task{}, fmt.Errorf("deadline: %w", err)
	}
	if task.PickedAt, err = optionalTime(raw.PickedAt); err != nil {
		return Task{}, fmt.Errorf("picked_at: %w", err)
	}
	if task.UnclaimedAt, err = optionalTime(raw.UnclaimedAt); err != nil {
		return Task{}, fmt.Errorf("unclaimed_at: %w", err)
	}
	if task.LastCompleted, err = optionalTime(raw.LastCompleted); err != nil {
		return Task{}, fmt.Errorf("lastCompletedAt: %w", err)
	}
	if task.SnapshotAt, err = optionalTime(raw.SnapshotAt); err != nil {
		return Task{}, fmt.Errorf("points snapshot time: %w", err)
	}
	if raw.PointsSnapshot != nil {
		value := roundedNonnegative(*raw.PointsSnapshot)
		task.PointsSnapshot = &value
	}
	if raw.Recurrence != nil {
		task.Recurrence = Recurrence{Kind: raw.Recurrence.Kind, Days: raw.Recurrence.Days, Weekdays: raw.Recurrence.Weekdays, Paused: raw.Recurrence.Paused}
		task.LeadDays = raw.Recurrence.LeadDays
	}
	if task.Recurrence.Kind == "" {
		task.Recurrence.Kind = "none"
	}
	for _, rawSnapshot := range raw.PointsHistory {
		createdAt, err := requiredTime(rawSnapshot.Timestamp, "snapshot timestamp")
		if err != nil {
			return Task{}, err
		}
		username := strings.TrimSpace(rawSnapshot.Username)
		if username != "" {
			task.Snapshots = append(task.Snapshots, PointSnapshot{Points: roundedNonnegative(rawSnapshot.Points), Username: username, CreatedAt: createdAt})
		}
	}
	if raw.CompletionUndo != nil {
		undo, err := normalizeUndo(*raw.CompletionUndo)
		if err != nil {
			return Task{}, fmt.Errorf("completionUndo: %w", err)
		}
		task.CompletionUndo = undo
	}
	return task, nil
}

func normalizeUndo(raw rawCompletionUndo) (*CompletionUndo, error) {
	completedAt, err := requiredTime(raw.CompletedAt, "completedAt")
	if err != nil {
		return nil, err
	}
	if len(raw.PreviousTask) == 0 || string(raw.PreviousTask) == "null" {
		return nil, errors.New("previousTask is missing")
	}
	var previous rawTask
	if err := json.Unmarshal(raw.PreviousTask, &previous); err != nil {
		return nil, err
	}
	previousTask, err := normalizeTask(previous)
	if err != nil {
		return nil, fmt.Errorf("previousTask: %w", err)
	}
	undo := &CompletionUndo{
		CompletionID: strings.TrimSpace(raw.CompletionID), CompletedAt: completedAt,
		Previous: UndoTask{
			State: previousTask.State, ScheduledAt: previousTask.ScheduledAt, Picker: previousTask.Picker,
			PickedAt: previousTask.PickedAt, LastCompleted: previousTask.LastCompleted,
			PointsSnapshot: previousTask.PointsSnapshot, SnapshotBy: previousTask.SnapshotBy,
			SnapshotAt: previousTask.SnapshotAt, UnclaimedAt: previousTask.UnclaimedAt,
		},
	}
	if raw.Award != nil {
		undo.AwardUser = strings.TrimSpace(raw.Award.User)
		if undo.CompletionID == "" {
			undo.CompletionID = strings.TrimSpace(raw.Award.CompletionID)
		}
	}
	return undo, nil
}

func normalizeUser(key string, raw rawUser) (User, []string, error) {
	username := strings.TrimSpace(raw.Username)
	if username == "" {
		username = strings.TrimSpace(key)
	}
	if username == "" {
		return User{}, nil, errors.New("legacy user has no username")
	}
	user := User{Username: username, PasswordHash: raw.PasswordHash, Points: raw.Points}
	warnings := []string{}
	for index, rawEntry := range raw.History {
		occurredAt, err := requiredTime(rawEntry.Timestamp, "history timestamp")
		if err != nil {
			return User{}, nil, fmt.Errorf("user %q history %d: %w", username, index, err)
		}
		taskID, taskTitle := parseCompletionReason(rawEntry.Reason)
		user.History = append(user.History, PointEntry{
			OccurredAt: occurredAt, Points: max(rawEntry.Points, 0), Reason: rawEntry.Reason,
			CompletionID: rawEntry.CompletionID, TaskID: taskID, TaskTitle: taskTitle,
		})
	}
	if raw.CreatedAt != "" {
		createdAt, err := requiredTime(raw.CreatedAt, "created_at")
		if err != nil {
			return User{}, nil, fmt.Errorf("user %q: %w", username, err)
		}
		user.CreatedAt = createdAt
	} else if len(user.History) > 0 {
		user.CreatedAt = user.History[0].OccurredAt
		for _, entry := range user.History[1:] {
			if entry.OccurredAt.Before(user.CreatedAt) {
				user.CreatedAt = entry.OccurredAt
			}
		}
		warnings = append(warnings, fmt.Sprintf("user %q had no created_at; used earliest point entry", username))
	} else {
		user.CreatedAt = time.Unix(0, 0).UTC()
		warnings = append(warnings, fmt.Sprintf("user %q had no created_at; used Unix epoch", username))
	}
	if raw.PasswordChangedAt != nil {
		changedAt, err := optionalTime(raw.PasswordChangedAt)
		if err != nil {
			return User{}, nil, fmt.Errorf("user %q password_changed_at: %w", username, err)
		}
		user.PasswordChangedAt = changedAt
	}
	return user, warnings, nil
}

func ensureReferencedUsers(bundle *Bundle) {
	known := make(map[string]struct{}, len(bundle.Users))
	for _, user := range bundle.Users {
		known[user.Username] = struct{}{}
	}
	add := func(username string, createdAt time.Time) {
		username = strings.TrimSpace(username)
		if username == "" {
			return
		}
		if _, exists := known[username]; exists {
			return
		}
		known[username] = struct{}{}
		bundle.Users = append(bundle.Users, User{Username: username, CreatedAt: createdAt})
		bundle.Report.SyntheticUsers++
		bundle.Report.Warnings = append(bundle.Report.Warnings, fmt.Sprintf("created point-only user %q for task attribution", username))
	}
	for _, task := range bundle.Tasks {
		if task.CreatedBy != nil {
			add(*task.CreatedBy, task.CreatedAt)
		}
		if task.Picker != nil {
			add(*task.Picker, task.CreatedAt)
		}
		if task.SnapshotBy != nil {
			add(*task.SnapshotBy, task.CreatedAt)
		}
		for _, snapshot := range task.Snapshots {
			add(snapshot.Username, snapshot.CreatedAt)
		}
		if task.CompletionUndo != nil {
			add(task.CompletionUndo.AwardUser, task.CompletionUndo.CompletedAt)
		}
	}
	sort.Slice(bundle.Users, func(i, j int) bool { return bundle.Users[i].Username < bundle.Users[j].Username })
}

func validate(bundle *Bundle) error {
	validStates := map[string]bool{"Waiting": true, "Ready": true, "InProgress": true, "Blocked": true, "Suspended": true, "Done": true}
	ids := make(map[string]struct{}, len(bundle.Tasks))
	for _, task := range bundle.Tasks {
		if _, duplicate := ids[task.ID]; duplicate {
			return fmt.Errorf("duplicate task id %q", task.ID)
		}
		ids[task.ID] = struct{}{}
		if !validStates[task.State] {
			return fmt.Errorf("task %q has invalid state %q", task.ID, task.State)
		}
		if task.Recurrence.Kind != "none" && task.Recurrence.Kind != "rolling" && task.Recurrence.Kind != "anchored" {
			return fmt.Errorf("task %q has invalid recurrence %q", task.ID, task.Recurrence.Kind)
		}
		if task.Recurrence.Kind == "rolling" && task.Recurrence.Days <= 0 {
			return fmt.Errorf("task %q has rolling recurrence without positive days", task.ID)
		}
		if task.Recurrence.Kind == "anchored" && task.Recurrence.Days <= 0 && len(task.Recurrence.Weekdays) == 0 {
			return fmt.Errorf("task %q has anchored recurrence without an interval or weekdays", task.ID)
		}
		for _, weekday := range task.Recurrence.Weekdays {
			if weekday < 0 || weekday > 6 {
				return fmt.Errorf("task %q has invalid recurrence weekday %d", task.ID, weekday)
			}
		}
		if task.PointsSnapshot != nil && (task.SnapshotBy == nil || task.SnapshotAt == nil) {
			return fmt.Errorf("task %q has incomplete current point snapshot metadata", task.ID)
		}
		if task.LeadDays < 0 {
			return fmt.Errorf("task %q has a negative lead time", task.ID)
		}
	}
	usernames := make(map[string]struct{}, len(bundle.Users))
	for _, user := range bundle.Users {
		if _, duplicate := usernames[user.Username]; duplicate {
			return fmt.Errorf("duplicate user %q", user.Username)
		}
		usernames[user.Username] = struct{}{}
		total := 0
		for _, entry := range user.History {
			total += entry.Points
		}
		if total != user.Points {
			return fmt.Errorf("user %q point total is %d but history sums to %d", user.Username, user.Points, total)
		}
	}
	for _, task := range bundle.Tasks {
		if task.RemedyFor != nil {
			if *task.RemedyFor == task.ID {
				return fmt.Errorf("task %q cannot be its own remedy", task.ID)
			}
			if _, exists := ids[*task.RemedyFor]; !exists {
				return fmt.Errorf("task %q references missing remedy parent %q", task.ID, *task.RemedyFor)
			}
		}
		for _, dependencyID := range task.Dependencies {
			if _, exists := ids[dependencyID]; !exists {
				return fmt.Errorf("task %q references missing dependency %q", task.ID, dependencyID)
			}
		}
	}
	visiting := make(map[string]bool, len(bundle.Tasks))
	visited := make(map[string]bool, len(bundle.Tasks))
	byID := make(map[string]Task, len(bundle.Tasks))
	for _, task := range bundle.Tasks {
		byID[task.ID] = task
	}
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("dependency cycle includes task %q", id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		for _, dependencyID := range byID[id].Dependencies {
			if err := visit(dependencyID); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	for id := range byID {
		if err := visit(id); err != nil {
			return err
		}
	}
	for state, limit := range bundle.Limits {
		if !validStates[state] || (limit != nil && *limit < 0) {
			return fmt.Errorf("invalid WIP limit for %q", state)
		}
	}
	return nil
}

func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func requiredTime(value, field string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, fmt.Errorf("%s is required", field)
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func optionalTime(value *string) (*time.Time, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return nil, err
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func cleanString(value *string) *string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	cleaned := strings.TrimSpace(*value)
	return &cleaned
}

func cleanStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func roundedNonnegative(value float64) int {
	if value <= 0 {
		return 0
	}
	return int(value + 0.5)
}

func parseCompletionReason(reason string) (string, string) {
	const prefix = "Completed task "
	if !strings.HasPrefix(reason, prefix) {
		return "", reason
	}
	remainder := strings.TrimPrefix(reason, prefix)
	separator := strings.Index(remainder, " (")
	if separator < 0 {
		return strings.TrimSpace(remainder), ""
	}
	return strings.TrimSpace(remainder[:separator]), strings.TrimSuffix(remainder[separator+2:], ")")
}
