#!/usr/bin/env bash
set -euo pipefail

panel_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd -- "$panel_dir"
work_dir=$(mktemp -d)
trap 'rm -rf -- "$work_dir"' EXIT
export PATH="$panel_dir/bin/.go/bin:$panel_dir/bin:$PATH"
"$panel_dir/bin/buf" generate --output "$work_dir"
diff --recursive --unified "$work_dir/gen" gen
