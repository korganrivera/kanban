# Migration And Rollback

The Node board remains the production source until the Go board passes the
final browser review. The importer never changes the Node data.

## Rehearsal Result

The 2026-08-05 rehearsal used the current Node data and a temporary Go
database:

- 222 tasks imported
- 8 users imported, including one attribution-only synthetic user
- 315 completion point entries imported
- 276 claim-time point snapshots imported
- 12 current completion undo records converted
- 1 undo record skipped because its task was edited after completion
- All foreign keys, ownership rules, point totals, WIP rows, and dependency
  cycles passed the post-import audit

The importer also preserves the existing bcrypt password hashes. Attribution-
only users have no password and cannot log in.

## Final Cutover

1. Announce a short write freeze and stop the Node service.
2. Run the Node tests and its final backup command.
3. Run `go test ./...`, build all three Go binaries, and stop any prototype Go
   process.
4. Run the importer once without `--apply` and review every warning.
5. Run the importer with `--apply --replace`. Keep the generated pre-import
   SQLite backup.
6. Run `bin/kanban-admin audit --data-dir data`.
7. Confirm task, user, point-entry, point-snapshot, and undo counts against the
   dry-run report.
8. Start the Go service and verify login, all six columns, claim/release,
   direct and claimed completion, undo, scheduling, recurrence, dependencies,
   block notes, remedies, deletion, WIP limits, points, history, and live
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
used successfully through at least one backup-and-restore drill.
