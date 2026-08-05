# Kanban Go

An isolated Go rewrite of the existing kanban board. It uses a single SQLite
database and embeds the browser interface in the executable.

This is a prototype. It does not read or modify the original kanban project or
its task data.

## Run

Go 1.26.5 or newer is required.

```sh
go run ./cmd/kanban
```

Open <http://127.0.0.1:3100>. The database is created at `data/kanban.db`.
The first visit offers account registration. After the first account is
created, further registration is disabled by default.

Configuration is available through environment variables:

```sh
KANBAN_ADDR=127.0.0.1:3100 \
KANBAN_DATA_DIR=/path/to/data \
ALLOW_REGISTRATION=0 \
COOKIE_SECURE=0 \
go run ./cmd/kanban
```

Set `ALLOW_REGISTRATION=1` only while additional accounts should be creatable.
Set `COOKIE_SECURE=1` when the browser reaches the board through HTTPS.

## Current scope

- Waiting, Ready, In Progress, Blocked, Suspended, and Done views
- Claim, release, block, unblock, complete, exact completion undo, and guarded
  deletion commands
- Scheduled tasks with configurable lead time
- Rolling and anchored recurring tasks, including selected weekdays
- Dependency-driven suspension with cycle protection
- Configurable WIP limits enforced against effective column state
- Automatic priority scoring from due dates and downstream dependencies
- Time-critical task ordering and blocked-task remedy creation
- Automatic claim release when a task leaves In Progress, while Done retains
  completion ownership
- Optimistic version checks and transactional updates
- Embedded browser assets and live board refresh through server-sent events
- Account registration, login, logout, rolling sessions, and password rotation
- Authenticated creator, claimant, and completion attribution
- Claim-time priority snapshots with transactional point awards and exact undo
- Durable completion history with a one-year activity heatmap
- Ready-time, overdue, deadline, creator, age, and completion metadata
- Per-account browser palettes and visible live-connection state

## Legacy import

The importer validates the Node board without writing anything by default:

```sh
go run ./cmd/kanban-import \
  --source /path/to/node-kanban/server/data
```

Stop the Go server before applying an import. Applying to an empty destination
requires `--apply`; replacing any existing Go users, tasks, or point history
also requires the explicit `--replace` flag:

```sh
go run ./cmd/kanban-import \
  --source /path/to/node-kanban/server/data \
  --data-dir /path/to/go-kanban/data \
  --apply --replace
```

Every applied import first creates a consistent database under
`DATA_DIR/backups/pre-import-*.db`. The import itself is transactional and
reports source and destination counts, invariant failures, and any legacy undo
record that could not be converted safely.

## Operations

Build the operational binaries with the server:

```sh
go build -o bin/kanban-go ./cmd/kanban
go build -o bin/kanban-admin ./cmd/kanban-admin
go build -o bin/kanban-import ./cmd/kanban-import
```

Audit the live database, create a consistent local backup, and verify the
published backup with:

```sh
bin/kanban-admin audit --data-dir data
bin/kanban-admin backup \
  --data-dir data \
  --destination /path/outside/data \
  --retention 30
bin/kanban-admin verify --backup /path/outside/data/latest
```

Each backup is published atomically with a SHA-256 manifest, an audited SQLite
database, an atomic `latest` symlink, and bounded retention. Local backups do
not protect against loss of the whole machine, so copy the backup directory to
independent storage when one becomes available.

Run `./scripts/install-service.sh` to install the persistent user service and
six-hour backup timer. See [systemd/README.md](systemd/README.md) for service,
configuration, and restore details. The final cutover and rollback procedure is
in [MIGRATION.md](MIGRATION.md), and the completed evidence review is in
[PARITY_REVIEW.md](PARITY_REVIEW.md).

## Test

```sh
go test ./...
```
