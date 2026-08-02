# Automated backups

The timer runs the checked-in backup script every six hours. Its destination
must be outside the live data directory, preferably on another disk or a
mounted remote filesystem.

```bash
mkdir -p ~/.config/kanban ~/.config/systemd/user
cp kanban-backup.service kanban-backup.timer ~/.config/systemd/user/
printf 'KANBAN_BACKUP_DEST=/absolute/path/to/backups\n' \
  > ~/.config/kanban/backup.env
chmod 600 ~/.config/kanban/backup.env
systemctl --user daemon-reload
systemctl --user enable --now kanban-backup.timer
systemctl --user start kanban-backup.service
```

Verify the latest backup after installation:

```bash
cd ~/code/kanban/server
npm run backup:verify -- /absolute/path/to/backups/latest
systemctl --user list-timers kanban-backup.timer
```

Optional settings in `~/.config/kanban/backup.env`:

```text
KANBAN_DATA_DIR=/absolute/path/to/live-data
KANBAN_BACKUP_RETENTION=30
```
