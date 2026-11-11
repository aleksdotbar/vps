#!/usr/bin/env bash
set -euo pipefail

# Both the Go test and its transient services belong to one resource budget.
# An enclosing scope alone would not cover services started through the manager.
panel_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
panel_test_id=$(cat /proc/sys/kernel/random/uuid)
panel_test_slice="panel-inspection-${panel_test_id}.slice"
panel_systemctl=${PANEL_TEST_SYSTEMCTL:-systemctl}
panel_systemd_run=${PANEL_TEST_SYSTEMD_RUN:-systemd-run}
panel_test_created=false

cleanup() {
  local result=$?
  trap - EXIT
  if [[ "$panel_test_created" != true ]]; then
    printf 'Inspection slice setup did not complete: %s\n' "$panel_test_slice" >&2
    exit "$result"
  fi
  if ! "$panel_systemctl" --user stop "$panel_test_slice"; then
    printf '%s\n' 'Could not stop the owned inspection test slice.' >&2
    result=1
  fi
  if ! "$panel_systemctl" --user revert "$panel_test_slice"; then
    printf '%s\n' 'Could not remove the owned inspection test slice settings.' >&2
    result=1
  fi
  exit "$result"
}

trap cleanup EXIT
# Slice names load implicitly, even without a unit file. Require an inactive,
# unconfigured slice before adding settings for this fresh random identity.
panel_slice_active=$("$panel_systemctl" --user show "$panel_test_slice" -p ActiveState --value)
panel_slice_fragment=$("$panel_systemctl" --user show "$panel_test_slice" -p FragmentPath --value)
panel_slice_dropins=$("$panel_systemctl" --user show "$panel_test_slice" -p DropInPaths --value)
test "$panel_slice_active" = inactive
test -z "$panel_slice_fragment"
test -z "$panel_slice_dropins"
"$panel_systemctl" --user set-property --runtime "$panel_test_slice" MemoryMax=6G CPUQuota=200%
panel_test_created=true
test "$("$panel_systemctl" --user show "$panel_test_slice" -p MemoryMax --value)" = 6442450944
test "$("$panel_systemctl" --user show "$panel_test_slice" -p CPUQuotaPerSecUSec --value)" = 2s

cd -- "$panel_dir"
"$panel_systemd_run" --user --scope --quiet --slice="$panel_test_slice" \
  --property=MemoryMax=6G --property=CPUQuota=200% --property=RuntimeMaxSec=35min \
  env PANEL_TEST_INSPECTION_SLICE="$panel_test_slice" GOMAXPROCS=2 GOFLAGS=-p=2 GOTOOLCHAIN=local \
  "$panel_dir/bin/.go/bin/go" test -tags=inspectionintegration -count=1 \
  -run '^(TestNativeInspection|TestInspectionFailure)' ./internal/hostsetup
