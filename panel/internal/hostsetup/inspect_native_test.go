//go:build inspectionintegration

package hostsetup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func inspectionFixtureTools(t *testing.T) inspectionTools {
	t.Helper()
	slice := os.Getenv("PANEL_TEST_INSPECTION_SLICE")
	if slice == "" {
		t.Fatal("native inspection fixture requires a bounded PANEL_TEST_INSPECTION_SLICE")
	}
	cgroup, err := os.ReadFile("/proc/self/cgroup")
	if err != nil || !bytes.Contains(cgroup, []byte("/"+slice+"/")) {
		t.Fatal("Go fixture is not inside its declared shared bounded slice")
	}
	return inspectionTools{systemdRun: nativeTool(t, "PANEL_TEST_SYSTEMD_RUN", "systemd-run"), systemctl: nativeTool(t, "PANEL_TEST_SYSTEMCTL", "systemctl"), userManager: true, fixtureSlice: slice, memoryMaxBytes: 2 << 30}
}

func TestNativeInspectionBoundsOutputAndKeepsServiceWithinFixtureSlice(t *testing.T) {
	tools := inspectionFixtureTools(t)
	data, err := runSourceInspection(t.Context(), tools, uuid.NewString(), []string{"/usr/bin/cat", "/proc/self/cgroup"})
	if err != nil || !bytes.Contains(data, []byte("/"+tools.fixtureSlice+"/")) {
		t.Fatalf("native service escaped shared fixture limit or failed sandbox: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	command := "/usr/bin/head -c " + strconv.Itoa(maxInspectionBytes+1) + " /dev/zero; /usr/bin/sleep 30"
	operation := uuid.NewString()
	data, err = runSourceInspection(ctx, tools, operation, []string{"/usr/bin/sh", "-c", command})
	if !errors.Is(err, ErrInspection) || !errors.Is(err, context.Canceled) || len(data) != 0 || ctx.Err() != nil {
		t.Fatalf("output overflow did not cancel before caller deadline or leaked data: %v", err)
	}
	assertInspectionTerminated(t, tools, "panel-inspect-"+operation+".service")
}

func assertInspectionTerminated(t *testing.T, tools inspectionTools, unit string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cgroup, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	var serviceGroup string
	for line := range strings.SplitSeq(string(cgroup), "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok {
			serviceGroup = filepath.Join(filepath.Dir(strings.TrimPrefix(path, "/")), unit, "cgroup.events")
		}
	}
	if serviceGroup == "" {
		t.Fatal("unified fixture cgroup missing")
	}
	groups, err := os.OpenRoot("/sys/fs/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = groups.Close() }()
	for {
		query := nativeCommand(ctx, tools.systemctl, "--user", "show", "--property=LoadState", "--property=ActiveState", "--", unit)
		query.Env = append(query.Env, "XDG_RUNTIME_DIR="+userManagerRuntimeDirectory())
		var output inspectionOutput
		query.Stdout = &output
		_ = query.Run()
		state := output.buffer.String()
		terminal := strings.Contains(state, "ActiveState=inactive") || strings.Contains(state, "ActiveState=failed")
		group, groupError := groups.ReadFile(serviceGroup)
		if terminal && (errors.Is(groupError, os.ErrNotExist) || (groupError == nil && bytes.Contains(group, []byte("populated 0")))) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("owned inspection unit or descendants did not become terminal")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestInspectionFailureNeverStopsPriorSameNameUnit(t *testing.T) {
	tools := inspectionFixtureTools(t)
	operation := uuid.NewString()
	unit := "panel-inspect-" + operation + ".service"
	process := nativeCommand(t.Context(), tools.systemdRun, "--user", "--slice="+tools.fixtureSlice, "--unit="+unit, "--description=Prior inspection fixture",
		"--quiet", "--collect", "--service-type=exec", "--property=MemoryMax=128M", "--property=CPUQuota=200%", "--property=RuntimeMaxSec=30", "--property=TimeoutStopSec=1", "--", "/usr/bin/sleep", "30")
	process.Env = append(process.Env, "XDG_RUNTIME_DIR="+userManagerRuntimeDirectory())
	if process.Run() != nil {
		t.Fatal("cannot start prior native inspection fixture")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		stop := nativeCommand(cleanup, tools.systemctl, "--user", "stop", "--", unit)
		stop.Env = append(stop.Env, "XDG_RUNTIME_DIR="+userManagerRuntimeDirectory())
		if stop.Run() != nil {
			t.Error("prior native fixture cleanup did not finish")
		}
	}()
	data, err := runSourceInspection(t.Context(), tools, operation, []string{"/usr/bin/true"})
	if !errors.Is(err, ErrInspection) || len(data) != 0 {
		t.Fatalf("existing native unit was replaced or exposed output: %v", err)
	}
	query := nativeCommand(t.Context(), tools.systemctl, "--user", "show", "--property=Description", "--property=ActiveState", "--", unit)
	query.Env = append(query.Env, "XDG_RUNTIME_DIR="+userManagerRuntimeDirectory())
	var output inspectionOutput
	query.Stdout = &output
	if query.Run() != nil || !strings.Contains(output.buffer.String(), "Description=Prior inspection fixture") || !strings.Contains(output.buffer.String(), "ActiveState=active") {
		t.Fatal("failed new attempt stopped or changed prior same-name unit")
	}
}
