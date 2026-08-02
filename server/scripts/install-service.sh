#!/usr/bin/env bash
set -euo pipefail

server_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source_unit="$server_dir/systemd/kanban-server.service"
unit_dir="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
installed_unit="$unit_dir/kanban-server.service"

mkdir -p "$unit_dir"
install -m 0644 "$source_unit" "$installed_unit"

if [[ "$(systemctl --user show kanban-server.service -p Transient --value 2>/dev/null || true)" == "yes" ]]; then
  systemctl --user stop kanban-server.service
fi

systemctl --user daemon-reload
systemctl --user enable --now kanban-server.service
systemctl --user is-active --quiet kanban-server.service

echo "Installed and started kanban-server.service"
