# User services

Run the installer from the repository:

```sh
./scripts/install-service.sh
```

It builds the server, admin tool, and importer; installs hardened user units;
starts the server; enables a six-hour backup timer; and creates the first local
backup. It does not require root. The generated units contain the repository's
absolute path, so rerun the installer after moving the repository.

Configuration files are created with mode `0600`:

- `~/.config/kanban-go/server.env` controls `KANBAN_ADDR`,
  `ALLOW_REGISTRATION`, and `COOKIE_SECURE`.
- `~/.config/kanban-go/backup.env` controls `KANBAN_DATA_DIR`,
  `KANBAN_BACKUP_DEST`, and `KANBAN_BACKUP_RETENTION`.

The default backup destination is
`~/.local/state/kanban-go/backups`, outside the live data directory. Keeping a
second copy on another disk or host remains necessary for protection from a
machine-wide failure.

Useful checks:

```sh
systemctl --user status kanban-go.service
systemctl --user list-timers kanban-go-backup.timer
bin/kanban-admin audit --data-dir data
bin/kanban-admin verify --backup ~/.local/state/kanban-go/backups/latest
curl http://127.0.0.1:3100/healthz
```

To keep user services running after logout, enable lingering once where the
host permits it:

```sh
loginctl enable-linger "$USER"
```

For a restore, stop the server, verify the selected backup, preserve the
current `data/kanban.db`, copy the verified backup's `kanban.db` into `data/`,
set its mode to `0600`, run `kanban-admin audit`, and then start the server.
