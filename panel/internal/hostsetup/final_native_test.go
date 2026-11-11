//go:build finalintegration

package hostsetup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNativeFinalOwnsDetachedServiceAndAbsoluteRecoveryTimer(t *testing.T) {
	slice := os.Getenv("PANEL_TEST_INSPECTION_SLICE")
	cgroup, err := os.ReadFile("/proc/self/cgroup")
	if err != nil || slice == "" || !bytes.Contains(cgroup, []byte("/"+slice+"/")) {
		t.Fatal("native enrollment fixture requires its shared bounded test slice")
	}
	enrollment := t.TempDir()
	bin := filepath.Join(enrollment, "sw", "bin")
	if os.MkdirAll(bin, 0700) != nil {
		t.Fatal("fixture directory failed")
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	runtime := "XDG_RUNTIME_DIR=" + quote(userManagerRuntimeDirectory()) + "; export XDG_RUNTIME_DIR\n"
	for _, tool := range []struct{ name, environment, extra string }{{"systemctl", "PANEL_TEST_SYSTEMCTL", ""}, {"systemd-run", "PANEL_TEST_SYSTEMD_RUN", "--slice=" + quote(slice)}} {
		real := os.Getenv(tool.environment)
		if real == "" {
			real = tool.name
		}
		real, err := exec.LookPath(real)
		if err != nil {
			t.Fatal(err)
		}
		script := "#!/bin/sh\n" + runtime + "exec " + quote(real) + " --user " + tool.extra + " \"$@\"\n"
		// #nosec G306 -- Executable public fixture wrappers contain only fixed native tool paths and this test's unique slice.
		if os.WriteFile(filepath.Join(bin, tool.name), []byte(script), 0700) != nil {
			t.Fatal("fixture tool failed")
		}
	}
	if os.Symlink("/usr/bin/env", filepath.Join(bin, "env")) != nil {
		t.Fatal("fixture env failed")
	}
	if err := os.Symlink("/usr/bin/true", filepath.Join(bin, "panel")); err != nil {
		t.Fatal(err)
	}
	intent := finalIntent{OperationID: uuid.NewString(), LaunchID: uuid.NewString(), EnrollmentSystem: enrollment, RecoveryDeadline: time.Now().Add(6 * time.Second).Unix()}
	if !finalUnitsAbsent(t.Context(), intent) {
		t.Fatal("fresh fixed operation units not absent")
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 20*time.Second)
		defer cancel()
		for _, name := range []string{finalUnitName(intent, false) + ".service", finalUnitName(intent, true) + ".timer", finalUnitName(intent, true) + ".service"} {
			_, _ = runtimeProbe(cleanup, bin+"/systemctl", "stop", "--", name)
		}
	})
	if err := launchFinalUnit(t.Context(), intent, true); err != nil {
		t.Fatal(err)
	}
	foreign := intent
	foreign.LaunchID = uuid.NewString()
	if err := stopFinalGuard(t.Context(), foreign); !errors.Is(err, ErrFinal) {
		t.Fatal("foreign timer ownership accepted")
	}
	timer, err := observeEnrollmentUnit(t.Context(), enrollment, finalUnitName(intent, true)+".timer")
	if err != nil || timer.active != "active" || timer.description != finalDescription(intent, true) {
		t.Fatalf("timer was stopped by foreign ownership check: %v", err)
	}
	if err := launchFinalUnit(t.Context(), intent, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(12 * time.Second)
	for {
		main, mainErr := ownedFinalUnit(t.Context(), intent, false)
		recovery, recoveryErr := ownedFinalUnit(t.Context(), intent, true)
		if mainErr == nil && recoveryErr == nil && main.sub == "exited" && recovery.sub == "exited" && main.result == "success" && recovery.result == "success" && main.exit == "0" && recovery.exit == "0" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("detached service/absolute timer not observed terminal: %v %v", mainErr, recoveryErr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	output, err := runtimeProbe(t.Context(), bin+"/systemctl", "show", "--property=Slice,MemoryMax,CPUQuotaPerSecUSec,RuntimeMaxUSec,TimeoutStopUSec,KillMode", "--", finalUnitName(intent, false)+".service")
	if err != nil || !strings.Contains(string(output), "Slice="+slice) || !strings.Contains(string(output), "MemoryMax="+strconv.FormatInt(2<<30, 10)) || !strings.Contains(string(output), "CPUQuotaPerSecUSec=2s") || !strings.Contains(string(output), "RuntimeMaxUSec=10min") || !strings.Contains(string(output), "TimeoutStopUSec=30s") || !strings.Contains(string(output), "KillMode=control-group") {
		t.Fatalf("detached service resource/cleanup policy not native: %v %s", err, output)
	}
}
