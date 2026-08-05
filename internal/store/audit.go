package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"kanban-go/internal/board"
)

type AuditReport struct {
	OK            bool           `json:"ok"`
	SchemaVersion int            `json:"schemaVersion"`
	Counts        map[string]int `json:"counts"`
	Findings      []string       `json:"findings"`
}

func (store *Store) Audit(ctx context.Context) (*AuditReport, error) {
	return auditDatabase(ctx, store.db)
}

func AuditPath(ctx context.Context, path string) (*AuditReport, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(absPath); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=foreign_keys(1)&_pragma=query_only(1)&_pragma=busy_timeout(5000)", filepath.ToSlash(absPath))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return nil, err
	}
	return auditDatabase(ctx, db)
}

func auditDatabase(ctx context.Context, db *sql.DB) (*AuditReport, error) {
	report := &AuditReport{Counts: make(map[string]int)}
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&report.SchemaVersion); err != nil {
		return nil, err
	}
	if report.SchemaVersion != len(migrations) {
		report.Findings = append(report.Findings, fmt.Sprintf(
			"schema version is %d; this binary expects %d", report.SchemaVersion, len(migrations),
		))
		return finishAudit(report), nil
	}

	integrityRows, err := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return nil, err
	}
	for integrityRows.Next() {
		var result string
		if err := integrityRows.Scan(&result); err != nil {
			integrityRows.Close()
			return nil, err
		}
		if result != "ok" {
			report.Findings = append(report.Findings, "SQLite integrity: "+result)
		}
	}
	if err := integrityRows.Close(); err != nil {
		return nil, err
	}

	foreignRows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return nil, err
	}
	for foreignRows.Next() {
		var table, parent string
		var rowID sql.NullInt64
		var foreignKey int
		if err := foreignRows.Scan(&table, &rowID, &parent, &foreignKey); err != nil {
			foreignRows.Close()
			return nil, err
		}
		report.Findings = append(report.Findings, fmt.Sprintf(
			"foreign key failure in %s row %d referencing %s (%d)", table, rowID.Int64, parent, foreignKey,
		))
	}
	if err := foreignRows.Close(); err != nil {
		return nil, err
	}

	counts := map[string]string{
		"tasks":             `SELECT COUNT(*) FROM tasks`,
		"users":             `SELECT COUNT(*) FROM users`,
		"completionEntries": `SELECT COUNT(*) FROM completion_entries`,
		"occurrences":       `SELECT COUNT(*) FROM task_occurrences`,
		"sessions":          `SELECT COUNT(*) FROM sessions`,
	}
	for name, query := range counts {
		var count int
		if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return nil, err
		}
		report.Counts[name] = count
	}

	if err := appendQueryFindings(ctx, db, report, `
		SELECT id || ' has lifecycle ' || lifecycle || ' with owner ' || COALESCE(claimed_by, '<none>')
		FROM tasks
		WHERE (lifecycle NOT IN ('InProgress', 'Done') AND claimed_by IS NOT NULL)
		   OR (lifecycle = 'InProgress' AND (claimed_by IS NULL OR claimed_by = ''))`,
		"claim invariant: "); err != nil {
		return nil, err
	}
	if err := appendQueryFindings(ctx, db, report, `
		SELECT o.task_id FROM task_occurrences o
		JOIN tasks t ON t.id = o.task_id
		WHERE o.outcome = 'completed' AND o.undone_at IS NULL AND o.result_version = t.version
		GROUP BY o.task_id HAVING COUNT(*) > 1`,
		"multiple current completion undo records for task "); err != nil {
		return nil, err
	}
	if err := appendQueryFindings(ctx, db, report, `
		SELECT o.task_id FROM task_occurrences o
		JOIN tasks t ON t.id = o.task_id
		JOIN completion_entries c ON c.id = o.completion_entry_id
		WHERE o.outcome = 'completed' AND o.undone_at IS NULL
		  AND o.result_version = t.version AND c.reversed_at IS NOT NULL`,
		"current completion undo references reversed history for task "); err != nil {
		return nil, err
	}
	var wipRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wip_limits`).Scan(&wipRows); err != nil {
		return nil, err
	}
	if wipRows != 6 {
		report.Findings = append(report.Findings, fmt.Sprintf("WIP limits contain %d rows; expected 6", wipRows))
	}

	tasks, err := listTasks(ctx, db)
	if err != nil {
		return nil, err
	}
	for _, cycle := range dependencyCycles(tasks) {
		report.Findings = append(report.Findings, "dependency cycle: "+strings.Join(cycle, " -> "))
	}
	return finishAudit(report), nil
}

func appendQueryFindings(ctx context.Context, db *sql.DB, report *AuditReport, query, prefix string) error {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var finding string
		if err := rows.Scan(&finding); err != nil {
			return err
		}
		report.Findings = append(report.Findings, prefix+finding)
	}
	return rows.Err()
}

func dependencyCycles(tasks []*board.Task) [][]string {
	graph := make(map[string][]string, len(tasks))
	for _, task := range tasks {
		graph[task.ID] = task.Dependencies
	}
	state := make(map[string]int, len(tasks))
	stack := make([]string, 0, len(tasks))
	cycles := make([][]string, 0)
	var visit func(string)
	visit = func(id string) {
		if state[id] == 2 {
			return
		}
		if state[id] == 1 {
			start := 0
			for index, stackID := range stack {
				if stackID == id {
					start = index
					break
				}
			}
			cycle := append([]string(nil), stack[start:]...)
			cycles = append(cycles, append(cycle, id))
			return
		}
		state[id] = 1
		stack = append(stack, id)
		for _, dependencyID := range graph[id] {
			visit(dependencyID)
		}
		stack = stack[:len(stack)-1]
		state[id] = 2
	}
	for id := range graph {
		visit(id)
	}
	return cycles
}

func finishAudit(report *AuditReport) *AuditReport {
	report.OK = len(report.Findings) == 0
	if report.Findings == nil {
		report.Findings = []string{}
	}
	return report
}
