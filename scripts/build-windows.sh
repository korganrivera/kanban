#!/usr/bin/env bash
set -euo pipefail

kanban_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
go_command=${GO:-go}
output_dir=${1:-"$kanban_root/dist/windows"}
payload_path="$kanban_root/cmd/kanban-installer/payload/kanban.exe"
version=${KANBAN_VERSION:-0.1.0-preview}

mkdir -p "$output_dir"
trap 'rm -f "$payload_path"' EXIT

common_ldflags="-s -w -H windowsgui -X main.version=$version"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 "$go_command" -C "$kanban_root" build \
  -trimpath -ldflags "$common_ldflags" -o "$payload_path" ./cmd/kanban

cp "$payload_path" "$output_dir/Kanban-Portable-windows-amd64.exe"

CGO_ENABLED=0 GOOS=windows GOARCH=amd64 "$go_command" -C "$kanban_root" build \
  -trimpath -ldflags "$common_ldflags" \
  -o "$output_dir/Kanban-Setup-windows-amd64.exe" ./cmd/kanban-installer

(
  cd "$output_dir"
  sha256sum Kanban-Portable-windows-amd64.exe Kanban-Setup-windows-amd64.exe > SHA256SUMS.txt
)

printf 'Windows artifacts written to %s\n' "$output_dir"
