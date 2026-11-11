#!/usr/bin/env bash
set -euo pipefail

panel_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
export PATH="$panel_dir/bin/.go/bin:$panel_dir/bin:$PATH"
export GOTOOLCHAIN=local GOMAXPROCS=2 GOFLAGS=-p=2
export PANEL_SOURCE
PANEL_SOURCE=$(cd -- "$panel_dir/.." && pwd)
output="$PANEL_SOURCE/.local-integration/native-deployment"
mkdir -p -- "$output"
export PANEL_NATIVE_BINARY="$output/panel" PANEL_NATIVE_TEST="$output/deployment.test"
cd -- "$panel_dir"
CGO_ENABLED=0 go build -trimpath -o "$PANEL_NATIVE_BINARY" ./cmd/panel
CGO_ENABLED=0 go test -c -o "$PANEL_NATIVE_TEST" ./internal/deployment
nix --extra-experimental-features 'nix-command flakes' build --impure \
  --max-jobs 1 --cores 1 --print-build-logs --no-link --print-out-paths --expr '
    let
      source = builtins.getEnv "PANEL_SOURCE";
      pkgs = (builtins.getFlake ("path:" + source + "/nix")).inputs.nixpkgs.legacyPackages.x86_64-linux;
    in import (source + "/nix/tests/deployment-runtime.nix") {
      inherit pkgs;
      panelBinary = builtins.path { path = builtins.toPath (builtins.getEnv "PANEL_NATIVE_BINARY"); name = "panel"; };
      testBinary = builtins.path { path = builtins.toPath (builtins.getEnv "PANEL_NATIVE_TEST"); name = "deployment-test"; };
    }'
