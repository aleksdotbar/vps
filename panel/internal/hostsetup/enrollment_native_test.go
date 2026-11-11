//go:build enrollmentintegration

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

func TestNativeEnrollmentOwnsDetachedServiceAndAbsoluteRecoveryTimer(t *testing.T) {
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
		real := nativeTool(t, tool.environment, tool.name)
		script := "#!/bin/sh\n" + runtime + "exec " + quote(real) + " --user " + tool.extra + " \"$@\"\n"
		// #nosec G306 -- Executable public fixture wrappers contain only fixed native tool paths and this test's unique slice.
		if os.WriteFile(filepath.Join(bin, tool.name), []byte(script), 0700) != nil {
			t.Fatal("fixture tool failed")
		}
	}
	if os.Symlink("/usr/bin/env", filepath.Join(bin, "env")) != nil {
		t.Fatal("fixture env failed")
	}
	intent := enrollmentIntent{OperationID: uuid.NewString(), LaunchID: uuid.NewString(), EnrollmentSystem: enrollment, RecoveryDeadline: time.Now().Add(6 * time.Second).Unix()}
	if !enrollmentUnitsAbsent(t.Context(), intent) {
		t.Fatal("fresh fixed operation units not absent")
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 20*time.Second)
		defer cancel()
		for _, name := range []string{enrollmentUnitName(intent, false) + ".service", enrollmentUnitName(intent, true) + ".timer", enrollmentUnitName(intent, true) + ".service"} {
			_, _ = runtimeProbe(cleanup, bin+"/systemctl", "stop", "--", name)
		}
	})
	if err := launchEnrollmentUnit(t.Context(), intent, "/usr/bin/true", true); err != nil {
		t.Fatal(err)
	}
	foreign := intent
	foreign.LaunchID = uuid.NewString()
	if err := stopEnrollmentGuard(t.Context(), foreign); !errors.Is(err, ErrEnrollment) {
		t.Fatal("foreign timer ownership accepted")
	}
	timer, err := observeEnrollmentUnit(t.Context(), enrollment, enrollmentUnitName(intent, true)+".timer")
	if err != nil || timer.active != "active" || timer.description != enrollmentDescription(intent, true) {
		t.Fatalf("timer was stopped by foreign ownership check: %v", err)
	}
	if err := launchEnrollmentUnit(t.Context(), intent, "/usr/bin/true", false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(12 * time.Second)
	for {
		main, mainErr := ownedEnrollmentUnit(t.Context(), intent, false)
		recovery, recoveryErr := ownedEnrollmentUnit(t.Context(), intent, true)
		if mainErr == nil && recoveryErr == nil && main.sub == "exited" && recovery.sub == "exited" && main.result == "success" && recovery.result == "success" && main.exit == "0" && recovery.exit == "0" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("detached service/absolute timer not observed terminal: %v %v", mainErr, recoveryErr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	output, err := runtimeProbe(t.Context(), bin+"/systemctl", "show", "--property=Slice,MemoryMax,CPUQuotaPerSecUSec,RuntimeMaxUSec,TimeoutStopUSec,KillMode", "--", enrollmentUnitName(intent, false)+".service")
	if err != nil || !strings.Contains(string(output), "Slice="+slice) || !strings.Contains(string(output), "MemoryMax="+strconv.FormatInt(2<<30, 10)) || !strings.Contains(string(output), "CPUQuotaPerSecUSec=2s") || !strings.Contains(string(output), "RuntimeMaxUSec=10min") || !strings.Contains(string(output), "TimeoutStopUSec=30s") || !strings.Contains(string(output), "KillMode=control-group") {
		t.Fatalf("detached service resource/cleanup policy not native: %v %s", err, output)
	}
}
