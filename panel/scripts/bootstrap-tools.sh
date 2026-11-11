#!/usr/bin/env bash
set -euo pipefail

if [[ $(uname -s) != Linux || $(uname -m) != x86_64 ]]; then
  echo 'Pinned bootstrap currently supports Linux x86_64 only.' >&2
  exit 1
fi

panel_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
tools_dir="$panel_dir/bin"
work_dir=$(mktemp -d)
trap 'rm -rf -- "$work_dir"' EXIT
mkdir -p -- "$tools_dir"

fetch() {
  local url=$1 checksum=$2 destination=$3
  curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
    --connect-timeout 15 --max-time 300 "$url" -o "$destination"
  printf '%s  %s\n' "$checksum" "$destination" | sha256sum --check --status
}

fetch https://go.dev/dl/go1.27.3.linux-amd64.tar.gz \
  65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40 \
  "$work_dir/go.tar.gz"
tar -xzf "$work_dir/go.tar.gz" -C "$work_dir"
# Replace only this script's managed tool directory.
rm -rf -- "$tools_dir/.go"
mv -- "$work_dir/go" "$tools_dir/.go"

fetch https://github.com/bufbuild/buf/releases/download/v1.75.0/buf-Linux-x86_64 \
  fad1e38d6909bbb583a4da81d8c02a01046e2cd663bde7e337dd24277393764b \
  "$work_dir/buf"
install -m 0755 "$work_dir/buf" "$tools_dir/buf"

fetch https://github.com/sqlc-dev/sqlc/releases/download/v1.31.1/sqlc_1.31.1_linux_amd64.tar.gz \
  497ae4fcdfa64c5b0c311ffe4c2bd991e43991e82e5367792ed78bc2dca27354 \
  "$work_dir/sqlc.tar.gz"
tar -xzf "$work_dir/sqlc.tar.gz" -C "$work_dir" sqlc
install -m 0755 "$work_dir/sqlc" "$tools_dir/sqlc"

fetch https://github.com/golangci/golangci-lint/releases/download/v2.15.0/golangci-lint-2.15.0-linux-amd64.tar.gz \
  9b7fe6a6bf6334b98decf8d86b11415ea7889afdabdd1959fdce23af2a9ebcea \
  "$work_dir/golangci-lint.tar.gz"
tar -xzf "$work_dir/golangci-lint.tar.gz" -C "$work_dir" \
  golangci-lint-2.15.0-linux-amd64/golangci-lint
install -m 0755 "$work_dir/golangci-lint-2.15.0-linux-amd64/golangci-lint" "$tools_dir/golangci-lint"

fetch https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/gitleaks_8.30.1_linux_x64.tar.gz \
  551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb \
  "$work_dir/gitleaks.tar.gz"
tar -xzf "$work_dir/gitleaks.tar.gz" -C "$work_dir" gitleaks
install -m 0755 "$work_dir/gitleaks" "$tools_dir/gitleaks"
