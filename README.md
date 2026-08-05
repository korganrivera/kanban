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

Migration from the existing board remains to be implemented.

## Test

```sh
go test ./...
```
