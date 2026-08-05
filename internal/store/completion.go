package store

import (
	"context"
	"time"
)

type CompletionEntry struct {
	ID         string    `json:"id"`
	TaskID     string    `json:"taskId"`
	TaskTitle  string    `json:"taskTitle"`
	OccurredAt time.Time `json:"occurredAt"`
}

func (store *Store) CompletionHistory(ctx context.Context, username string) ([]CompletionEntry, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT id, task_id, task_title, completed_at
		FROM completion_entries
		WHERE username = ? AND reversed_at IS NULL
		ORDER BY completed_at DESC, id DESC`, username,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]CompletionEntry, 0)
	for rows.Next() {
		var entry CompletionEntry
		var occurredAt string
		if err := rows.Scan(
			&entry.ID, &entry.TaskID, &entry.TaskTitle, &occurredAt,
		); err != nil {
			return nil, err
		}
		entry.OccurredAt, err = parseTime(occurredAt)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}
