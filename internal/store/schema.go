package store

const schema = `
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS tasks (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    lifecycle TEXT NOT NULL CHECK (lifecycle IN ('Ready', 'InProgress', 'Blocked', 'Done')),
    scheduled_at TEXT,
    lead_days INTEGER NOT NULL DEFAULT 0 CHECK (lead_days >= 0),
    recurrence_kind TEXT NOT NULL DEFAULT 'none' CHECK (recurrence_kind IN ('none', 'rolling', 'anchored')),
    recurrence_days INTEGER NOT NULL DEFAULT 0 CHECK (recurrence_days >= 0),
    recurrence_paused INTEGER NOT NULL DEFAULT 0 CHECK (recurrence_paused IN (0, 1)),
    claimed_by TEXT,
    claimed_at TEXT,
    block_note TEXT NOT NULL DEFAULT '',
    last_completed_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS task_dependencies (
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    depends_on_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
    PRIMARY KEY (task_id, depends_on_id),
    CHECK (task_id <> depends_on_id)
);

CREATE TABLE IF NOT EXISTS task_occurrences (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    outcome TEXT NOT NULL CHECK (outcome IN ('completed', 'skipped')),
    actor TEXT NOT NULL,
    occurred_at TEXT NOT NULL,
    scheduled_for TEXT,
    previous_lifecycle TEXT NOT NULL,
    previous_scheduled_at TEXT,
    previous_claimed_by TEXT,
    previous_claimed_at TEXT,
    previous_last_completed_at TEXT,
    result_version INTEGER NOT NULL,
    undone_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_dependencies_target
    ON task_dependencies(depends_on_id);
CREATE INDEX IF NOT EXISTS idx_occurrences_task
    ON task_occurrences(task_id, occurred_at DESC);
`
