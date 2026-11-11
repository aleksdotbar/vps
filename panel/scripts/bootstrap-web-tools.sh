#!/usr/bin/env bash
set -euo pipefail

if [[ $(uname -s) != Linux || $(uname -m) != x86_64 ]]; then
  echo 'Pinned frontend tools currently support Linux x86_64 only.' >&2
  exit 1
fi
panel_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
tools_dir="$panel_dir/bin/.web"
work_dir=$(mktemp -d)
trap 'rm -rf -- "$work_dir"' EXIT

curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
  --connect-timeout 15 --max-time 300 \
  https://nodejs.org/dist/v24.22.2/node-v24.22.2-linux-x64.tar.xz -o "$work_dir/node.tar.xz"
printf '%s  %s\n' 48cc0e21e048790e54c0cf5749c2725005313d652651c40b7a21feb9697aaf75 \
  "$work_dir/node.tar.xz" | sha256sum --check --status
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
  --connect-timeout 15 --max-time 300 \
  https://registry.npmjs.org/@pnpm/exe.linux-x64/-/exe.linux-x64-12.16.1.tgz -o "$work_dir/pnpm.tar.gz"
python3 - "$work_dir/pnpm.tar.gz" <<'PY'
import base64
import hashlib
from pathlib import Path
import sys
expected = 'RK//8ZLcXGW8A2k8bQOXZDBN1Qu92zN4dYnG1bzj1zoQ1fkZ6jNzZ1AvXtsfGVma795CJtvoaF5bLXf4l/FIUw=='
with Path(sys.argv[1]).open('rb') as stream:
    actual = hashlib.file_digest(stream, 'sha512').digest()
if actual != base64.b64decode(expected, validate=True):
    raise SystemExit('Pinned pnpm checksum mismatch')
PY
tar -xJf "$work_dir/node.tar.xz" -C "$work_dir" --no-same-owner
tar -xzf "$work_dir/pnpm.tar.gz" -C "$work_dir" --no-same-owner
test "$("$work_dir/node-v24.22.2-linux-x64/bin/node" --version)" = v24.22.2
test "$("$work_dir/package/pnpm" --version)" = 12.16.1
mkdir -p -- "$panel_dir/bin"
# This script owns only the ignored frontend tool directory.
rm -rf -- "$tools_dir"
mkdir -p -- "$tools_dir/pnpm"
mv -- "$work_dir/node-v24.22.2-linux-x64" "$tools_dir/node"
install -m 0755 "$work_dir/package/pnpm" "$tools_dir/pnpm/pnpm"
