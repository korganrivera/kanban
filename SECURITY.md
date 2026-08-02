# Security notes

## Historical task and account data

Older commits contain runtime task data and bcrypt password hashes. The current
tree no longer tracks those files, but removing a file from the current commit
does not remove it from Git history.

Treat the existing password hashes as exposed and rotate every account password
after the history is cleaned. History rewriting must be coordinated because it
changes commit IDs and requires a force-push plus fresh clones or careful resets
for every user of the repository.

A maintainer can use `git filter-repo` to remove these paths after making a
separate backup and notifying all repository users:

```bash
git filter-repo \
  --path server/data/tasks.json \
  --path server/data/users.json \
  --path server/data/wip_limits.json \
  --path server/backups/ \
  --invert-paths
```

Review the rewritten repository before force-pushing all affected branches and
tags. Do not consider the cleanup complete until old remote references are gone
and all passwords have been changed.
