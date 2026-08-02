# Security notes

## Historical task and account data

Before August 2, 2026, commits contained runtime task data and bcrypt password
hashes. The `main` branch was rewritten and force-pushed on that date to remove
the runtime data, old data backups, and committed dependency tree from every
reachable commit.

Treat the historical password hashes as exposed until every account password
has been changed. Any clone made before the rewrite can still contain the old
objects and should be replaced with a fresh clone rather than pushed again.

Each user can rotate their own password under `Settings` > `Account`. A
successful change invalidates that account's other sessions.
