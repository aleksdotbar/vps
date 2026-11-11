package hostsetup

import (
	"context"
	"os"
	"strconv"
	"time"
)

func finalUnitName(intent finalIntent, recovery bool) string {
	prefix := "panel-final-"
	if recovery {
		prefix = "panel-final-recover-"
	}
	return prefix + intent.OperationID
}
func finalDescription(intent finalIntent, recovery bool) string {
	label := "Panel final activation "
	if recovery {
		label = "Panel final recovery "
	}
	return label + intent.OperationID + " " + intent.LaunchID
}
func ownedFinalUnit(ctx context.Context, intent finalIntent, recovery bool) (enrollmentUnit, error) {
	unit, err := observeEnrollmentUnit(ctx, intent.EnrollmentSystem, finalUnitName(intent, recovery)+".service")
	if err != nil || unit.load != "loaded" || unit.description != finalDescription(intent, recovery) {
		return unit, ErrFinal
	}
	return unit, nil
}
func finalUnitsAbsent(ctx context.Context, intent finalIntent) bool {
	for _, name := range []string{finalUnitName(intent, false) + ".service", finalUnitName(intent, true) + ".service", finalUnitName(intent, true) + ".timer"} {
		unit, err := observeEnrollmentUnit(ctx, intent.EnrollmentSystem, name)
		if err != nil || unit.load != "not-found" || unit.active != "inactive" {
			return false
		}
	}
	return true
}
func finalServiceArguments(intent finalIntent, recovery bool) []string {
	arguments := []string{"--quiet", "--no-ask-password", "--service-type=exec", "--expand-environment=no", "--unit=" + finalUnitName(intent, recovery), "--description=" + finalDescription(intent, recovery), "--property=MemoryMax=2G", "--property=CPUQuota=200%", "--property=TasksMax=256", "--property=RuntimeMaxSec=600", "--property=TimeoutStopSec=30", "--property=KillMode=control-group", "--property=UMask=0077", "--property=StandardOutput=null", "--property=StandardError=null", "--property=RemainAfterExit=yes"}
	branch := "final-runner"
	if recovery {
		branch = "final-recover"
		arguments = append(arguments, "--on-calendar=@"+strconv.FormatInt(intent.RecoveryDeadline, 10), "--timer-property=AccuracySec=1s", "--timer-property=Description="+finalDescription(intent, true))
	}
	arguments = append(arguments, "--", intent.EnrollmentSystem+"/sw/bin/env", "-i")
	arguments = append(arguments, importEnvironment()...)
	return append(arguments, "NIX_PATH=", intent.EnrollmentSystem+"/sw/bin/panel", "install", branch, "--operation-id", intent.OperationID)
}
func launchFinalUnit(ctx context.Context, intent finalIntent, recovery bool) error {
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if nativeCommand(bounded, intent.EnrollmentSystem+"/sw/bin/systemd-run", finalServiceArguments(intent, recovery)...).Run() != nil {
		return ErrFinal
	}
	return nil
}
func stopFinalGuard(ctx context.Context, intent finalIntent) error {
	timer, err := observeEnrollmentUnit(ctx, intent.EnrollmentSystem, finalUnitName(intent, true)+".timer")
	if err != nil || timer.description != finalDescription(intent, true) {
		return ErrFinal
	}
	if _, err := runtimeProbe(ctx, intent.EnrollmentSystem+"/sw/bin/systemctl", "stop", "--", finalUnitName(intent, true)+".timer"); err != nil {
		return ErrFinal
	}
	return nil
}
func nativeFinalSwitch(ctx context.Context, setup *finalSetup, intent finalIntent, target string) error {
	// Same pinned nixos-rebuild store-path primitives as enrollment; each effect
	// is preceded by this operation's durable phase, never enrollment's receipt.
	links, err := readEnrollmentLinks()
	marker, markerErr := os.Stat(target + "/nixos-version")
	if err != nil || !finalOwnsObservedLinks(intent, setup.enrolled, links) || (target != intent.FinalSystem && target != intent.EnrollmentSystem) || markerErr != nil || marker == nil || !marker.Mode().IsRegular() {
		return ErrUnresolved
	}
	phase := "profile_updating"
	if target == intent.EnrollmentSystem {
		phase = "recovering"
	}
	if writeFinalPhase(ctx, setup.root, intent, phase) != nil {
		return ErrUnresolved
	}
	process := nativeCommand(ctx, intent.EnrollmentSystem+"/sw/bin/nix-env", "-p", "/nix/var/nix/profiles/system", "--set", target)
	process.Env = append(importEnvironment(), "NIX_PATH=")
	if process.Run() != nil {
		return ErrUnresolved
	}
	links, err = readEnrollmentLinks()
	if err != nil || !finalOwnsObservedLinks(intent, setup.enrolled, links) || links.profile != target {
		return ErrUnresolved
	}
	if target == intent.FinalSystem && writeFinalPhase(ctx, setup.root, intent, "switching") != nil {
		return ErrUnresolved
	}
	process = nativeCommand(ctx, target+"/bin/switch-to-configuration", "switch")
	process.Env = append(importEnvironment(), "NIX_PATH=", "NIXOS_INSTALL_BOOTLOADER=0")
	if process.Run() != nil {
		return ErrUnresolved
	}
	return nil
}
