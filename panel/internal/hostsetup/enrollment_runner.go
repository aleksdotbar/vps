package hostsetup

import (
	"context"
	"encoding/json"
	"os"
	"time"
)

// RunEnrollment is invoked only by the fixed root-owned transient service.
// It accepts no paths or commands; all effect inputs are reloaded from the
// protected immutable operation admitted by the trusted first-stage caller.
func RunEnrollment(ctx context.Context, operationID string, recovery bool) error {
	if !canonicalOperation(operationID) {
		return ErrIdentity
	}
	ctx, cancel := context.WithTimeout(ctx, 9*time.Minute)
	defer cancel()
	root, err := enrollmentStateRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	var stored importIntent
	if readState(root, "intent.json", &stored) != nil {
		return ErrIdentity
	}
	imported, err := readImportIntent(root, operationID, stored.Pins)
	if err != nil {
		return err
	}
	intent, err := readEnrollmentIntent(root, imported)
	if err != nil {
		return err
	}
	owned, err := ownedEnrollmentUnit(ctx, intent, recovery)
	if err != nil || owned.terminal() {
		return ErrEnrollment
	}
	if recovery {
		return recoverEnrollment(ctx, root, imported, intent)
	}
	lock, err := waitStateLock(ctx, root)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	phase, err := enrollmentPhase(root, intent)
	if err != nil || !enrollmentMayStart(intent, phase, time.Now()) {
		return ErrEnrollment
	}
	// Every failure after this durable boundary remains observable, and only the
	// separately supervised recovery runner may decide whether restoration is safe.
	if writeEnrollmentPhase(ctx, root, intent, "preparing") != nil {
		return ErrUnresolved
	}
	if err := prepareEnrollmentEffect(ctx, root, imported, intent); err != nil {
		_ = writeEnrollmentPhase(ctx, root, intent, "needs_attention")
		return err
	}
	if err := lock.Close(); err != nil {
		return ErrUnresolved
	}
	if err := nativeEnrollmentSwitch(ctx, root, intent, intent.EnrollmentSystem); err != nil {
		return err
	}
	lock, err = waitStateLock(ctx, root)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	links, err := readEnrollmentLinks()
	if err != nil || !enrollmentAt(intent, links, intent.EnrollmentSystem) {
		return ErrUnresolved
	}
	// Actual native account/SSH checks run against enrollment. Source bootstrap
	// allowances remain the reviewed policy; no private-access readiness is implied.
	input, err := retainedInspectionInput(ctx, root, imported)
	if err != nil {
		return err
	}
	previous, err := readEnrollmentInspection(ctx, root, "enrollment-inspection.json", intent.InspectionSHA256, imported)
	if err != nil {
		return err
	}
	candidatePacket, err := enrollmentSSHPacket(previous.Packet)
	if err != nil {
		return ErrUnresolved
	}
	if _, err := inspectRuntimeSSH(ctx, intent.EnrollmentSystem, intent.EnrollmentSystem, input, candidatePacket); err != nil {
		return ErrUnresolved
	}
	return writeEnrollmentPhase(ctx, root, intent, "awaiting_bootstrap")
}

func prepareEnrollmentEffect(ctx context.Context, root *os.Root, imported importIntent, intent enrollmentIntent) error {
	// Saved inspection is a caller binding, never authority to skip fresh checks.
	if _, err := readEnrollmentInspection(ctx, root, "enrollment-inspection.json", intent.InspectionSHA256, imported); err != nil {
		return err
	}
	fresh, err := inspectRetained(ctx, root, imported)
	if err != nil || fresh.InspectionSHA256 != intent.InspectionSHA256 || fresh.SystemPath != intent.SourceSystem {
		return ErrSourceMismatch
	}
	input, err := retainedInspectionInput(ctx, root, imported)
	if err != nil {
		return err
	}
	accounts, err := readRuntimeAccounts(ctx, accountRuntimeFiles{passwd: "/etc/passwd", group: "/etc/group", declaredUsers: "/var/lib/nixos/declarative-users", declaredGroups: "/var/lib/nixos/declarative-groups"})
	if err != nil {
		return ErrEnrollment
	}
	// A preexisting runner could already read its future runtime path under an
	// unproved old policy. Never activate new authority against such an identity.
	if _, exists := accounts.users["panel-runner"]; exists {
		return ErrEnrollment
	}
	policyData, err := runtimeProbe(ctx, intent.EnrollmentSystem+"/sw/bin/sshd", "-G", "-T", "-C", "user=root", "-f", "/etc/ssh/sshd_config")
	if err != nil {
		return ErrEnrollment
	}
	settings, err := parseSSHEffective(policyData)
	if err != nil || confirmEnrollmentLoginUsers(settings, input, accounts) != nil {
		return ErrEnrollment
	}
	parent, err := openSSHDirectory("/var/lib", 0)
	if err != nil {
		return ErrEnrollment
	}
	defer func() { _ = parent.Close() }()
	if !enrollmentBundleAbsent(parent, "panel-access") || !enrollmentBundleAbsent(root, enrollmentBundle) {
		return ErrEnrollment
	}
	if stageEnrollmentAccess(ctx, root, intent) != nil {
		return ErrEnrollment
	}
	links, err := readEnrollmentLinks()
	if err != nil || links.current != intent.SourceSystem || links.profile != intent.SourceSystem || links.booted != intent.SourceSystem {
		return ErrSourceMismatch
	}
	if writeEnrollmentPhase(ctx, root, intent, "publishing") != nil {
		return ErrUnresolved
	}
	if err := publishEnrollmentAccess(ctx, root, intent, parent); err != nil {
		return err
	}
	return writeEnrollmentPhase(ctx, root, intent, "published")
}

func recoverEnrollment(ctx context.Context, root *os.Root, imported importIntent, intent enrollmentIntent) error {
	main, err := ownedEnrollmentUnit(ctx, intent, false)
	if err != nil {
		// A missing unit after reboot or a lost launch is not proof that a switch
		// never began. Keep the retained operation unresolved rather than reapply.
		return ErrUnresolved
	}
	if !main.terminal() {
		return ErrUnresolved
	}
	lock, err := waitStateLock(ctx, root)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	phase, err := enrollmentPhase(root, intent)
	if err != nil {
		return err
	}
	if phase == "enrolled" || phase == "restored" {
		return nil
	}
	links, err := readEnrollmentLinks()
	if err != nil || !enrollmentOwnsLinks(intent, links) {
		return ErrUnresolved
	}
	if phase == "recovering" {
		return ErrUnresolved
	} // Never blindly repeat an interrupted restore.
	parent, err := openSSHDirectory("/var/lib", 0)
	if err != nil {
		return ErrEnrollment
	}
	defer func() { _ = parent.Close() }()
	if !enrollmentBundleAbsent(parent, "panel-access") && checkEnrollmentAccess(ctx, parent, "panel-access", intent.Files, true) != nil {
		return ErrUnresolved
	}
	if err := retainEnrollmentSource(ctx, imported, intent.SourceSystem); err != nil {
		return err
	}
	if writeEnrollmentPhase(ctx, root, intent, "recovering") != nil {
		return ErrUnresolved
	}
	if !enrollmentAt(intent, links, intent.SourceSystem) {
		if err := lock.Close(); err != nil {
			return ErrUnresolved
		}
		if nativeEnrollmentSwitch(ctx, root, intent, intent.SourceSystem) != nil {
			return ErrUnresolved
		}
		lock, err = waitStateLock(ctx, root)
		if err != nil {
			return err
		}
		defer func() { _ = lock.Close() }()
	}
	after, err := readEnrollmentLinks()
	if err != nil || !enrollmentAt(intent, after, intent.SourceSystem) {
		return ErrUnresolved
	}
	if err := removeEnrollmentAccess(ctx, parent, intent); err != nil {
		return err
	}
	return writeEnrollmentPhase(ctx, root, intent, "restored")
}

func enrollmentSSHPacket(previous []byte) ([]byte, error) {
	var packet sourcePacket
	if json.Unmarshal(previous, &packet) != nil {
		return nil, ErrEnrollment
	}
	// Fixed foundation projection appends its runtime authority path before the
	// stock home/rendered defaults. Native sshd still evaluates the actual policy.
	packet.SSH = json.RawMessage(`{"authorized_keys_files":["/var/lib/panel-access/%u/authorized_keys","%h/.ssh/authorized_keys","/etc/ssh/authorized_keys.d/%u"]}`)
	result, err := json.Marshal(packet)
	if err != nil {
		return nil, ErrEnrollment
	}
	return result, nil
}
