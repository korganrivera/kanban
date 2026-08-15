# Final Parity Review

Reviewed on 2026-08-06 against the current Node server, browser client, tests,
and legacy data. The Node project remained unchanged throughout the review.

## Route Equivalence

| Node route | Go equivalent |
| --- | --- |
| `POST /auth/register` | `POST /api/auth/register` |
| `POST /auth/login` | `POST /api/auth/login` |
| `POST /auth/change-password` | `POST /api/auth/change-password` |
| `POST /auth/logout` | `POST /api/auth/logout` |
| `GET /auth/whoami` | `GET /api/auth/me` |
| `GET /auth/registration-status` | `GET /api/auth/registration` |
| `GET /healthz` | `GET /healthz` |
| `GET /tasks` | `GET /api/tasks` |
| `POST /tasks` | `POST /api/tasks` |
| `PATCH /tasks/:id` | `PATCH /api/tasks/{id}` |
| `PATCH /tasks/:id/state` | Typed claim, release, block, unblock, complete, and undo commands |
| `POST /tasks/:id/undo-complete` | `POST /api/tasks/{id}/undo` |
| `POST /tasks/:id/remedy` | `POST /api/tasks/{id}/remedy` |
| `DELETE /tasks/:id` | `DELETE /api/tasks/{id}` |
| `GET /wip-limits` | `GET /api/wip-limits` |
| `PATCH /wip-limits` | `PATCH /api/wip-limits` |
| Authenticated WebSocket updates | Authenticated server-sent events |

The Go HTTP tests exercise every equivalent route and command. A real
two-session test verifies live updates, stale-edit rejection, password-change
revocation, and closure of the revoked session's event stream.

## Workflow Review

- All six columns use the same effective-state rules. Waiting and Suspended are
  derived rather than persisted as contradictory mutable states.
- Dragging and card commands cover Ready to In Progress, In Progress to Ready,
  Ready/In Progress to Blocked, Blocked to Ready, and In Progress to Done.
- Completion is available only after a task is claimed into In Progress; both
  the command API and drag/drop reject Ready-to-Done completion.
- Claims exist only in In Progress; Done keeps completion ownership.
- Scheduling, lead time, rolling recurrence, anchored intervals, anchored
  weekdays, paused recurrence, recurring dependency occurrences, deadlines,
  time-critical ordering, and automatic priority match the Node behavior.
- Completion undo restores exact task state and reverses only its matching
  history entry. Durable history survives deletion.
- Dependency cycles, stale writes, invalid WIP limits, unsafe prerequisite
  deletion, malformed requests, cross-origin mutations, and unauthenticated
  data access fail closed.
- Block notes and descriptions are compact on cards and remain complete in the
  task editor. Remedy creation and optional cleanup are transactional.
- Picker history is intentionally not migrated, matching the explicit project
  decision made before the rewrite.

## Rendered Review

Firefox 153 rendered an isolated 16-task board containing every column,
recurrence types, overdue and time-critical tasks, dependencies, claims,
completion, block notes, and remedies.

- At 1600x900, the document matched the viewport, all six columns were visible,
  and every visible card reported no internal overflow.
- At 390x844, the document again matched the viewport. The board alone owned
  its 1565-pixel horizontal extent and its vertical scroll; the top bar and
  column headers remained fixed without overlap.
- Horizontal and vertical scroll positions were exercised together through
  the final column.
- The mobile task editor and settings panel both fit the viewport width. Their
  complete forms scrolled vertically with no control overflow.

## Data And Recovery Review

The full legacy rehearsal imported 222 tasks, 8 users, 317 completion history
entries, and 14 safe undo records. Legacy point values and claim snapshots are
intentionally discarded. One stale undo was excluded because its task had
changed afterward. Post-import counts, foreign keys, claims, WIP rows, and
dependency invariants passed.

Backup tests cover consistent SQLite snapshots, SHA-256 corruption detection,
atomic publication, retention, and a standalone restore drill. The final
cutover and rollback sequence is in `MIGRATION.md`.

## Result

The Go rewrite has functional parity with the useful Node workflows, with the
documented improvements in state modeling, identity, validation, concurrency,
security, migration, testing, and recovery. It replaced the Node board in
production on 2026-08-15.

## Known Differences

- Waiting is derived from scheduling rather than manually assignable. Legacy
  undated Waiting tasks therefore import as Ready instead of preserving a
  mutable state that users cannot move back out of through the Node UI.
- Active columns use the displayed integer priority for stable ordering. The
  Node client used continuously changing fractional priority values, which
  could reorder equal-looking tasks during periodic refreshes.
- Anchored weekday recurrence currently preserves the stored UTC clock time.
  The Node server preserved its local clock time across daylight-saving
  changes. No migrated task currently uses anchored weekday recurrence.
- Fixed-interval anchored recurrence skips missed occurrences on completion and
  advances to the first cadence-aligned future date instead of renewing to an
  already elapsed date.
- Legacy points and picker-history data remain intentionally excluded.
