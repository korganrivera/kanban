#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
unit_dir=${XDG_CONFIG_HOME:-"$HOME/.config"}/systemd/user
config_dir=${XDG_CONFIG_HOME:-"$HOME/.config"}/kanban-go
backup_dir=${XDG_STATE_HOME:-"$HOME/.local/state"}/kanban-go/backups
go_command=${GO:-go}

mkdir -p "$root/bin" "$root/data" "$unit_dir" "$config_dir" "$backup_dir"
chmod 700 "$root/data" "$config_dir" "$backup_dir"

"$go_command" -C "$root" build -o "$root/bin/kanban-go" ./cmd/kanban
"$go_command" -C "$root" build -o "$root/bin/kanban-admin" ./cmd/kanban-admin
"$go_command" -C "$root" build -o "$root/bin/kanban-import" ./cmd/kanban-import

escaped_root=${root//\\/\\\\}
escaped_root=${escaped_root//&/\\&}
escaped_root=${escaped_root//|/\\|}
for unit in kanban-go.service kanban-go-backup.service; do
  sed "s|@KANBAN_ROOT@|$escaped_root|g" "$root/systemd/$unit.in" > "$unit_dir/$unit.tmp"
  chmod 600 "$unit_dir/$unit.tmp"
  mv "$unit_dir/$unit.tmp" "$unit_dir/$unit"
done
install -m 600 "$root/systemd/kanban-go-backup.timer" "$unit_dir/kanban-go-backup.timer"

if [[ ! -e "$config_dir/server.env" ]]; then
  printf '%s\n' '# ALLOW_REGISTRATION=1' '# COOKIE_SECURE=1' > "$config_dir/server.env"
  chmod 600 "$config_dir/server.env"
fi
if [[ ! -e "$config_dir/backup.env" ]]; then
  printf 'KANBAN_DATA_DIR=%s/data\nKANBAN_BACKUP_DEST=%s\nKANBAN_BACKUP_RETENTION=30\n' \
    "$root" "$backup_dir" > "$config_dir/backup.env"
  chmod 600 "$config_dir/backup.env"
fi

systemctl --user daemon-reload
systemctl --user enable --now kanban-go.service kanban-go-backup.timer
for _ in {1..30}; do
  [[ -f "$root/data/kanban.db" ]] && break
  sleep 1
done
systemctl --user start kanban-go-backup.service

printf 'Server: systemctl --user status kanban-go.service\n'
printf 'Backups: systemctl --user list-timers kanban-go-backup.timer\n'
