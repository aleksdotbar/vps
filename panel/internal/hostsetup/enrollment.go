package hostsetup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

// Enroll is the first-stage installer's only enrollment mutation. Public-key
// input comes from the trusted caller; no management private key enters target
// setup, the immutable artifact, command arguments or setup receipts.
func Enroll(ctx context.Context, operationID, buildSHA, inspectionSHA string, public []byte) (EnrollmentResult, error) {
	pins := adoptionPins{buildSHA}
	if !canonicalOperation(operationID) || !pins.valid() || !sha256Hex.MatchString(inspectionSHA) {
		return EnrollmentResult{}, ErrIdentity
	}
	root, err := enrollmentStateRoot()
	if err != nil {
		return EnrollmentResult{}, err
	}
	defer func() { _ = root.Close() }()
	imported, err := readImportIntent(root, operationID, pins)
	if err != nil {
		return EnrollmentResult{}, err
	}
	if existing, err := readEnrollmentIntent(root, imported); err == nil {
		if existing.InspectionSHA256 != inspectionSHA || !sameEnrollmentPublic(public, existing.ManagementKey) {
			return EnrollmentResult{}, ErrIdentity
		}
		return observeEnrollment(ctx, root, imported, existing)
	} else if !errors.Is(err, os.ErrNotExist) {
		return EnrollmentResult{}, err
	}
	lock, err := stateLock(root)
	if err != nil {
		return EnrollmentResult{}, err
	}
	defer func() { _ = lock.Close() }()
	if !enrollmentEvidenceAbsent(root) {
		return EnrollmentResult{}, ErrEnrollment
	}
	source, err := enrollmentInspection(ctx, root, inspectionSHA, imported)
	if err != nil {
		return EnrollmentResult{}, err
	}
	input, err := retainedInspectionInput(ctx, root, imported)
	if err != nil {
		return EnrollmentResult{}, err
	}
	files, key, fingerprint, err := enrollmentAccessPlan(public, input, source.SSH)
	if err != nil {
		return EnrollmentResult{}, err
	}
	links, err := readEnrollmentLinks()
	if err != nil || links.current != source.SystemPath || links.profile != source.SystemPath || links.booted != source.SystemPath || source.SystemPath == imported.Build.EnrollmentSystem {
		return EnrollmentResult{}, ErrSourceMismatch
	}
	parent, err := openSSHDirectory("/var/lib", 0)
	if err != nil {
		return EnrollmentResult{}, ErrEnrollment
	}
	defer func() { _ = parent.Close() }()
	if !enrollmentBundleAbsent(parent, "panel-access") {
		return EnrollmentResult{}, ErrEnrollment
	}
	intent := enrollmentIntent{OperationID: operationID, Pins: pins, InspectionSHA256: inspectionSHA, SourceSystem: source.SystemPath, EnrollmentSystem: imported.Build.EnrollmentSystem, ManagementKey: key, ManagementFingerprint: fingerprint, LaunchID: uuid.NewString(), RecoveryDeadline: time.Now().Add(12 * time.Minute).Unix(), Files: files}
	if !enrollmentUnitsAbsent(ctx, intent) {
		return EnrollmentResult{}, ErrEnrollment
	}
	if retainEnrollmentSource(ctx, imported, intent.SourceSystem) != nil {
		return EnrollmentResult{}, ErrEnrollment
	}
	if writeState(ctx, root, "enrollment.json", intent, false) != nil || retainEnrollmentInspection(ctx, root, source) != nil || writeEnrollmentPhase(ctx, root, intent, "starting") != nil {
		return EnrollmentResult{}, ErrUnresolved
	}
	// Arm recovery before starting the effectful runner. A lost launch response
	// never authorizes a second launch; the retained intent is now observation-only.
	if launchEnrollmentUnit(ctx, intent, imported.Build.PanelStorePath+"/bin/panel", true) != nil {
		return EnrollmentResult{}, ErrUnresolved
	}
	if err := lock.Close(); err != nil {
		return EnrollmentResult{}, ErrUnresolved
	}
	if launchEnrollmentUnit(ctx, intent, imported.Build.PanelStorePath+"/bin/panel", false) != nil {
		return EnrollmentResult{}, ErrUnresolved
	}
	return enrollmentResult(imported, intent, "starting", links), nil
}

func sameEnrollmentPublic(data []byte, canonical string) bool {
	if len(data) == 0 || len(data) > 1024 {
		return false
	}
	key, _, options, rest, err := ssh.ParseAuthorizedKey(data)
	return err == nil && len(options) == 0 && len(bytes.TrimSpace(rest)) == 0 && string(bytes.TrimSpace(ssh.MarshalAuthorizedKey(key))) == canonical
}

func enrollmentEvidenceAbsent(root *os.Root) bool {
	for _, name := range []string{"enrollment.json", "enrollment-receipt.json", "enrollment-inspection.json", enrollmentBundle} {
		if !enrollmentBundleAbsent(root, name) {
			return false
		}
	}
	return true
}

func EnrollmentStatus(ctx context.Context, operationID, buildSHA string) (EnrollmentResult, error) {
	pins := adoptionPins{buildSHA}
	if !canonicalOperation(operationID) || !pins.valid() {
		return EnrollmentResult{}, ErrIdentity
	}
	root, err := enrollmentStateRoot()
	if err != nil {
		return EnrollmentResult{}, err
	}
	defer func() { _ = root.Close() }()
	imported, err := readImportIntent(root, operationID, pins)
	if err != nil {
		return EnrollmentResult{}, err
	}
	intent, err := readEnrollmentIntent(root, imported)
	if errors.Is(err, os.ErrNotExist) {
		lock, err := stateLock(root)
		if err != nil {
			return EnrollmentResult{}, err
		}
		defer func() { _ = lock.Close() }()
		if !enrollmentEvidenceAbsent(root) {
			return EnrollmentResult{}, ErrEnrollment
		}
		var receipt importReceipt
		if readState(root, "receipt.json", &receipt) != nil || receipt.OperationID != operationID || receipt.Phase != "imported" {
			return EnrollmentResult{}, ErrEnrollment
		}
		links, err := readEnrollmentLinks()
		if err != nil {
			return EnrollmentResult{}, err
		}
		return enrollmentResult(imported, enrollmentIntent{}, "not_started", links), nil
	}
	if err != nil {
		return EnrollmentResult{}, err
	}
	return observeEnrollment(ctx, root, imported, intent)
}

func observeEnrollment(ctx context.Context, root *os.Root, imported importIntent, intent enrollmentIntent) (EnrollmentResult, error) {
	links, err := readEnrollmentLinks()
	if err != nil {
		return EnrollmentResult{}, err
	}
	phase, phaseErr := enrollmentPhase(root, intent)
	status := "needs_attention"
	// Missing or partial phase evidence is an observed unknown outcome. It
	// cannot authorize confirmation, a new switch or restoration.
	if phaseErr == nil && enrollmentOwnsLinks(intent, links) {
		switch phase {
		case "enrolled":
			if enrollmentAt(intent, links, intent.EnrollmentSystem) {
				status = "enrolled"
			}
		case "restored":
			if enrollmentAt(intent, links, intent.SourceSystem) {
				status = "restored"
			}
		default:
			unit, unitErr := ownedEnrollmentUnit(ctx, intent, false)
			if unitErr == nil {
				if !unit.terminal() {
					status = "enrolling"
				} else if phase == "awaiting_bootstrap" && unit.result == "success" && unit.exit == "0" && enrollmentAt(intent, links, intent.EnrollmentSystem) && time.Now().Unix() < intent.RecoveryDeadline {
					status = "awaiting_bootstrap"
				}
			}
			recovery, recoveryErr := ownedEnrollmentUnit(ctx, intent, true)
			if recoveryErr == nil && !recovery.terminal() {
				status = "recovering"
			}
		}
	}
	return enrollmentResult(imported, intent, status, links), nil
}

// ConfirmEnrollment is called only after the trusted launcher establishes a new
// pinned bootstrap SSH connection. It acknowledges enrollment, not private
// Tailscale access, panel readiness, a successful reboot or final lockdown.
func ConfirmEnrollment(ctx context.Context, operationID, buildSHA string) (EnrollmentResult, error) {
	pins := adoptionPins{buildSHA}
	if !canonicalOperation(operationID) || !pins.valid() {
		return EnrollmentResult{}, ErrIdentity
	}
	root, err := enrollmentStateRoot()
	if err != nil {
		return EnrollmentResult{}, err
	}
	defer func() { _ = root.Close() }()
	lock, err := stateLock(root)
	if err != nil {
		return EnrollmentResult{}, err
	}
	defer func() { _ = lock.Close() }()
	imported, err := readImportIntent(root, operationID, pins)
	if err != nil {
		return EnrollmentResult{}, err
	}
	intent, err := readEnrollmentIntent(root, imported)
	if err != nil {
		return EnrollmentResult{}, err
	}
	observed, err := observeEnrollment(ctx, root, imported, intent)
	if err != nil {
		return EnrollmentResult{}, err
	}
	if observed.Status == "enrolled" {
		return observed, nil
	}
	if observed.Status != "awaiting_bootstrap" {
		return EnrollmentResult{}, ErrEnrollment
	}
	if writeEnrollmentPhase(ctx, root, intent, "enrolled") != nil {
		return EnrollmentResult{}, ErrUnresolved
	}
	// An already-started recovery runner must acquire the same setup lock and
	// re-read enrolled before acting. Failure to stop its timer cannot undo this.
	if stopEnrollmentGuard(ctx, intent) != nil {
		return EnrollmentResult{}, ErrUnresolved
	}
	observed.Status = "enrolled"
	return observed, nil
}
