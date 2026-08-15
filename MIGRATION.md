# Migration And Rollback

## Cutover Status

The Go board became the production board on 2026-08-15 after successful parity,
browser, integrity, persistence, and backup checks.

- `kanban-go.service` is enabled and serves the board on `127.0.0.1:3100`.
- `kanban-server.service` is disabled, and the legacy Node data is unchanged.
- The final verified Node backup is at
  `~/.local/state/kanban-node-final/latest`.
- Go backups are retained under `~/.local/state/kanban-go/backups`, with a
  verified backup created every six hours by `kanban-go-backup.timer`.
- The Node implementation remains available through the `node-legacy-final`
  Git tag and the unchanged `/home/korgan/code/kanban` working directory.

The importer never writes to the Node data. The original cutover procedure is
retained below as an operational reference.

## Rehearsal Result

The 2026-08-05 rehearsal used the current Node data and a temporary Go
database:

- 222 tasks imported
- 8 users imported, including one attribution-only synthetic user
- 317 completion history entries imported without legacy scores
- 14 current completion undo records converted
- 1 undo record skipped because its task was edited after completion
- All foreign keys, ownership rules, WIP rows, and dependency
  cycles passed the post-import audit

The importer also preserves the existing bcrypt password hashes. Attribution-
only users have no password and cannot log in.

## Cutover Procedure

1. Announce a short write freeze and stop the Node service.
2. Run the Node tests and its final backup command.
3. Run `go test ./...`, build all three Go binaries, and stop any prototype Go
   process.
4. Run the importer once without `--apply` and review every warning.
5. Run the importer with `--apply --replace`. Keep the generated pre-import
   SQLite backup.
6. Run `bin/kanban-admin audit --data-dir data`.
7. Confirm task, user, completion-entry, and undo counts against the
   dry-run report.
8. Start the Go service and verify login, all six columns, claim/release,
   claim-before-complete, undo, scheduling, recurrence, dependencies,
   block notes, remedies, deletion, WIP limits, history, and live
   updates in desktop and mobile layouts.
9. Create and verify the first post-migration backup before ending the write
   freeze.

## Rollback

1. Stop `kanban-go.service` immediately and do not make more Go changes.
2. Restart the unchanged Node service and confirm its health endpoint and task
   count. This is the primary rollback because the import never writes Node
   data.
3. Preserve the failed Go database for diagnosis.
4. To retry without returning to Node, verify the importer's `pre-import-*.db`,
   replace `data/kanban.db` while the Go service is stopped, set mode `0600`,
   and run `kanban-admin audit` before restarting.

Do not delete the Node project or its final backup until the Go board has been
used successfully through at least one backup-and-restore drill. Local backups
also need an independent copy before the board is protected from total machine
or disk loss.
