#!/usr/bin/env bash
set -euo pipefail

panel_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
work_dir=$(mktemp -d)
trap 'rm -rf -- "$work_dir"' EXIT
mkdir -p "$work_dir/internal/store"
cp "$panel_dir/sqlc.yaml" "$work_dir/sqlc.yaml"
cp "$panel_dir/internal/store/"*.sql "$work_dir/internal/store/"
cp -R "$panel_dir/internal/store/migrations" "$work_dir/internal/store/migrations"
"$panel_dir/bin/sqlc" generate -f "$work_dir/sqlc.yaml"
for generated in "$work_dir/internal/store/"*.go; do
  diff --unified "$panel_dir/internal/store/$(basename -- "$generated")" "$generated"
done
