# Functional Parity Checklist

The existing Node kanban is the behavior specification for this rewrite. A
checked item is implemented in Go and covered by tests or a live workflow
check. Equivalent behavior may use a different implementation when the Go
design is simpler or safer.

## Board And Tasks

- [x] Waiting, Ready, In Progress, Blocked, Suspended, and Done columns
- [x] Derived Waiting and Suspended states
- [x] Claim, release, block, unblock, complete, and exact completion undo
- [x] Claims only in In Progress, with completion attribution retained in Done
- [x] Editable block notes shown on blocked cards and in the task editor
- [x] Scheduled due dates and lead-time readiness
- [x] Rolling recurrence
- [x] Anchored interval recurrence
- [x] Anchored weekday recurrence
- [x] Paused recurrence
- [x] Dependency suspension and recurring-dependency occurrence semantics
- [x] Dependency cycle prevention
- [x] Guarded deletion when dependents exist
- [x] Blocked-task remedy creation and parent suspension
- [x] Configurable WIP limits enforced against effective column state
- [x] Automatic priority from urgency and downstream importance
- [x] Time-critical ordering
- [x] Optimistic version checks for concurrent edits
- [x] Live board updates
- [x] Quick add and full task editor
- [x] Truncated card descriptions with full descriptions in the editor
- [x] Separate legacy `deadline` field for API and import fidelity
- [x] Created-by attribution on tasks and remedies
- [x] Priority snapshot metadata on claimed tasks
- [x] Remedy-aware dependency removal with optional remedy cleanup

## Accounts And Security

- [x] Initial account registration
- [x] Registration disabled after the first account unless explicitly enabled
- [x] Login and logout
- [x] Persistent rolling sessions with secure cookie settings
- [x] Authentication required for task data, WIP settings, and live events
- [x] Claims and creation attributed to the authenticated user
- [x] Password change with current-password verification
- [x] Password change revokes the user's other sessions and live connections
- [x] Authentication rate limiting and constant-work invalid login checks
- [x] Same-origin checks for mutating requests
- [x] Account controls in the browser UI

## Points And Completion History

- [x] Snapshot automatic priority when a task is claimed
- [x] Refresh snapshots for a different claimant or after the cooldown
- [x] Award the exact snapshot on completion
- [x] Reverse only the matching award when completion is undone
- [x] Preserve user completion history after task deletion
- [x] Show current user and point total in the top bar
- [x] Show claim snapshot and award state in task details
- [x] Completion heatmap, totals, and streak summaries

## Browser Experience

- [x] Login and registration screen
- [x] Live-connection status indicator
- [x] Ready-at and overdue indicators on cards
- [x] Recurrence-aware overdue calculation
- [x] Complete task metadata: creator, age, completion date, and owner
- [x] Palette selection persisted per browser user
- [x] Automatic time-boundary refresh without waiting up to one minute
- [ ] Recheck desktop and mobile layout after parity controls are present

## Data Migration

- [x] Dry-run importer for legacy tasks, users, and WIP limits
- [x] Preserve IDs, timestamps, ownership, recurrence, dependencies, and notes
- [x] Preserve bcrypt password hashes and user point history
- [x] Convert legacy completion undo data safely where possible
- [x] Validate source references and cycles before writing
- [x] Transactional import with a pre-import SQLite backup
- [x] Post-import count and invariant report

## Operations And Recovery

- [x] Restrictive data-directory and database file permissions
- [x] Consistent configuration for address, data path, registration, and cookies
- [ ] SQLite-consistent backup command
- [ ] SHA-256 backup manifest and verification command
- [ ] Atomic latest-backup pointer and retention pruning
- [ ] Database and domain-invariant audit command
- [ ] Persistent user systemd service with restart policy and hardening
- [ ] Automated backup service and timer
- [ ] Service installer and operating documentation

## Final Verification

- [ ] API parity tests for every legacy route or equivalent command
- [ ] Multi-session tests for login, revocation, live updates, and stale edits
- [x] Import fixture covering every legacy task shape
- [ ] Backup corruption and restore-drill tests
- [ ] Manual side-by-side workflow review before migration
- [ ] Final migration plan with rollback steps

## Implementation Order

1. Accounts, sessions, authenticated identity, and security controls
2. Created-by metadata, priority snapshots, points, and completion history
3. Remaining card metadata, overdue behavior, palettes, and connection status
4. Legacy importer and migration validation
5. Backup, audit, systemd service, and recovery documentation
6. Side-by-side parity review and migration rehearsal
