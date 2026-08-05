package board

import (
	"math"
	"time"
)

const (
	importanceK        = 0.5
	urgencyLambda      = 0.8
	urgencyMidpointDay = 3.0
)

func ComputePriorities(tasks []*Task, now time.Time) {
	byID := make(map[string]*Task, len(tasks))
	dependents := make(map[string][]string, len(tasks))
	for _, task := range tasks {
		byID[task.ID] = task
		dependents[task.ID] = nil
		task.Deadlock = false
	}
	for _, task := range tasks {
		if task.Lifecycle == LifecycleDone {
			continue
		}
		for _, dependencyID := range task.Dependencies {
			if _, exists := byID[dependencyID]; exists {
				dependents[dependencyID] = append(dependents[dependencyID], task.ID)
			}
		}
	}

	memo := make(map[string]float64, len(tasks))
	visiting := make(map[string]bool, len(tasks))
	var rawImportance func(string) float64
	rawImportance = func(id string) float64 {
		if value, exists := memo[id]; exists {
			return value
		}
		if visiting[id] {
			markCycle(id, byID, visiting)
			return 0
		}
		visiting[id] = true
		value := 0.0
		for _, dependentID := range dependents[id] {
			value += 1 + 0.5*rawImportance(dependentID)
		}
		visiting[id] = false
		if byID[id] != nil && byID[id].Deadlock {
			value = 0
		}
		memo[id] = value
		return value
	}

	for _, task := range tasks {
		raw := rawImportance(task.ID)
		task.Importance = importanceScore(raw)
		dueAt := task.Deadline
		if dueAt == nil {
			dueAt = task.ScheduledAt
		}
		task.Urgency = urgencyScore(dueAt, now)
		task.Priority = int(math.Round(1 + task.Importance + task.Urgency))
		if task.Deadlock {
			task.Importance = 0
			task.Priority = int(math.Round(1 + task.Urgency))
		}
	}
}

func markCycle(id string, byID map[string]*Task, visiting map[string]bool) {
	for candidateID, active := range visiting {
		if active && byID[candidateID] != nil {
			byID[candidateID].Deadlock = true
		}
	}
	if byID[id] != nil {
		byID[id].Deadlock = true
	}
}

func importanceScore(raw float64) float64 {
	if raw <= 0 {
		return 0
	}
	return 49.5 * raw / (raw + importanceK)
}

func urgencyScore(due *time.Time, now time.Time) float64 {
	if due == nil {
		return 0
	}
	days := due.Sub(now).Hours() / 24
	return 49.5 / (1 + math.Exp(urgencyLambda*(days-urgencyMidpointDay)))
}
