#!/usr/bin/env bash
set -euo pipefail

panel_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
export PATH="$panel_dir/bin/.web/node/bin:$panel_dir/bin/.web/pnpm:$PATH"
test "$(node --version)" = v24.22.2
test "$(pnpm --version)" = 12.16.1
export CI=true
cd -- "$panel_dir/web"
pnpm install --frozen-lockfile
pnpm typecheck
pnpm test:boundaries
pnpm lint
pnpm format:check
pnpm build
