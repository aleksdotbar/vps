package hostsetup

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"
)

type enrollmentUnit struct{ load, active, sub, result, description, exit string }

func (unit enrollmentUnit) terminal() bool {
	return unit.active == "inactive" || unit.active == "failed" || (unit.active == "active" && unit.sub == "exited")
}
func enrollmentUnitName(intent enrollmentIntent, recovery bool) string {
	prefix := "panel-enroll-"
	if recovery {
		prefix = "panel-enroll-recover-"
	}
	return prefix + intent.OperationID
}
func enrollmentDescription(intent enrollmentIntent, recovery bool) string {
	label := "Panel enrollment "
	if recovery {
		label = "Panel enrollment recovery "
	}
	return label + intent.OperationID + " " + intent.LaunchID
}
func observeEnrollmentUnit(ctx context.Context, enrollment, name string) (enrollmentUnit, error) {
	data, err := runtimeProbe(ctx, enrollment+"/sw/bin/systemctl", "show", "--property=LoadState,ActiveState,SubState,Result,Description,ExecMainStatus", "--", name)
	if err != nil {
		return enrollmentUnit{}, ErrEnrollment
	}
	var unit enrollmentUnit
	seen := map[string]bool{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || seen[key] {
			return unit, ErrEnrollment
		}
		seen[key] = true
		switch key {
		case "LoadState":
			unit.load = value
		case "ActiveState":
			unit.active = value
		case "SubState":
			unit.sub = value
		case "Result":
			unit.result = value
		case "Description":
			unit.description = value
		case "ExecMainStatus":
			unit.exit = value
		default:
			return unit, ErrEnrollment
		}
	}
	if !seen["LoadState"] || !seen["ActiveState"] || !seen["SubState"] || !seen["Description"] {
		return unit, ErrEnrollment
	}
	return unit, nil
}
func ownedEnrollmentUnit(ctx context.Context, intent enrollmentIntent, recovery bool) (enrollmentUnit, error) {
	unit, err := observeEnrollmentUnit(ctx, intent.EnrollmentSystem, enrollmentUnitName(intent, recovery)+".service")
	if err != nil || unit.load != "loaded" || unit.description != enrollmentDescription(intent, recovery) {
		return unit, ErrEnrollment
	}
	return unit, nil
}
func enrollmentUnitsAbsent(ctx context.Context, intent enrollmentIntent) bool {
	for _, name := range []string{enrollmentUnitName(intent, false) + ".service", enrollmentUnitName(intent, true) + ".service", enrollmentUnitName(intent, true) + ".timer"} {
		unit, err := observeEnrollmentUnit(ctx, intent.EnrollmentSystem, name)
		if err != nil || unit.load != "not-found" || unit.active != "inactive" {
			return false
		}
	}
	return true
}
func enrollmentServiceArguments(intent enrollmentIntent, binary string, recovery bool) []string {
	arguments := []string{"--quiet", "--no-ask-password", "--service-type=exec", "--expand-environment=no", "--unit=" + enrollmentUnitName(intent, recovery), "--description=" + enrollmentDescription(intent, recovery),
		"--property=MemoryMax=2G", "--property=CPUQuota=200%", "--property=TasksMax=256", "--property=RuntimeMaxSec=600", "--property=TimeoutStopSec=30", "--property=KillMode=control-group", "--property=UMask=0077", "--property=StandardOutput=null", "--property=StandardError=null", "--property=RemainAfterExit=yes"}
	branch := "enrollment-runner"
	if recovery {
		branch = "enrollment-recover"
		arguments = append(arguments, "--on-calendar=@"+strconv.FormatInt(intent.RecoveryDeadline, 10), "--timer-property=AccuracySec=1s", "--timer-property=Description="+enrollmentDescription(intent, true))
	}
	arguments = append(arguments, "--", intent.EnrollmentSystem+"/sw/bin/env", "-i")
	arguments = append(arguments, importEnvironment()...)
	arguments = append(arguments, "NIX_PATH=", binary, "install", branch, "--operation-id", intent.OperationID)
	return arguments
}
func launchEnrollmentUnit(ctx context.Context, intent enrollmentIntent, binary string, recovery bool) error {
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	process := nativeCommand(bounded, intent.EnrollmentSystem+"/sw/bin/systemd-run", enrollmentServiceArguments(intent, binary, recovery)...)
	if process.Run() != nil {
		return ErrEnrollment
	}
	return nil
}
func stopEnrollmentGuard(ctx context.Context, intent enrollmentIntent) error {
	timer, err := observeEnrollmentUnit(ctx, intent.EnrollmentSystem, enrollmentUnitName(intent, true)+".timer")
	if err != nil || timer.description != enrollmentDescription(intent, true) {
		return ErrEnrollment
	}
	if _, err := runtimeProbe(ctx, intent.EnrollmentSystem+"/sw/bin/systemctl", "stop", "--", enrollmentUnitName(intent, true)+".timer"); err != nil {
		return ErrEnrollment
	}
	return nil
}
func nativeEnrollmentSwitch(ctx context.Context, root *os.Root, intent enrollmentIntent, target string) error {
	// This is pinned nixos-rebuild's --store-path sequence without its second,
	// globally named transient service: validate the NixOS marker, set the native
	// system profile, then execute the already-built switch-to-configuration.
	info, err := os.Stat(target + "/nixos-version")
	if err != nil || info == nil || !info.Mode().IsRegular() {
		return ErrEnrollment
	}
	phase := "profile_updating"
	if target == intent.SourceSystem {
		phase = "recovering"
	}
	if writeEnrollmentPhase(ctx, root, intent, phase) != nil {
		return ErrUnresolved
	}
	profile := nativeCommand(ctx, intent.EnrollmentSystem+"/sw/bin/nix-env", "-p", "/nix/var/nix/profiles/system", "--set", target)
	profile.Env = append(importEnvironment(), "NIX_PATH=")
	if profile.Run() != nil {
		return ErrUnresolved
	}
	if target == intent.EnrollmentSystem && writeEnrollmentPhase(ctx, root, intent, "switching") != nil {
		return ErrUnresolved
	}
	process := nativeCommand(ctx, target+"/bin/switch-to-configuration", "switch")
	process.Env = append(importEnvironment(), "NIX_PATH=", "NIXOS_INSTALL_BOOTLOADER=0")
	if process.Run() != nil {
		return ErrUnresolved
	}
	return nil
}
