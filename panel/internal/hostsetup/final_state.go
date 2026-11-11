package hostsetup

import (
	"context"
	"errors"
	"os"
	"os/user"
	"strconv"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/foundation"
)

type finalSetup struct {
	root     *os.Root
	lock     *os.File
	imported importIntent
	enrolled enrollmentIntent
	input    foundation.Input
}

func (setup *finalSetup) close() {
	if setup.lock != nil {
		_ = setup.lock.Close()
	}
	if setup.root != nil {
		_ = setup.root.Close()
	}
}
func openFinalSetup(ctx context.Context, operation string, pins adoptionPins) (*finalSetup, error) {
	if !canonicalOperation(operation) || !pins.valid() {
		return nil, ErrIdentity
	}
	root, err := enrollmentStateRoot()
	if err != nil {
		return nil, err
	}
	lock, err := stateLock(root)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return loadFinalSetup(ctx, root, lock, operation, pins)
}

func loadFinalSetup(ctx context.Context, root *os.Root, lock *os.File, operation string, pins adoptionPins) (*finalSetup, error) {
	setup := &finalSetup{root: root, lock: lock}
	var err error
	setup.imported, err = readImportIntent(root, operation, pins)
	if err == nil {
		setup.enrolled, err = readEnrollmentIntent(root, setup.imported)
	}
	if err == nil {
		setup.input, err = retainedInspectionInput(ctx, root, setup.imported)
	}
	if err == nil {
		phase, phaseErr := enrollmentPhase(root, setup.enrolled)
		if phaseErr != nil || phase != "enrolled" || setup.input.Role != foundation.Panel {
			err = ErrFinal
		}
	}
	if err != nil {
		setup.close()
		return nil, err
	}
	return setup, nil
}

func confirmFinalOperator(operator string) error {
	// The trusted launcher establishes a new pinned private operator SSH session.
	// sudo's authenticated actor proves the local administrator identity; it does
	// not let this target infer desktop reachability or out-of-band recovery.
	if os.Geteuid() != 0 || os.Getenv("SUDO_USER") != operator {
		return ErrFinal
	}
	account, err := user.Lookup(operator)
	if err != nil || account == nil || account.Uid == "0" || account.Uid != os.Getenv("SUDO_UID") || account.Gid != os.Getenv("SUDO_GID") {
		return ErrFinal
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil || uid == 0 {
		return ErrFinal
	}
	return nil
}
func readFinalIntent(setup *finalSetup) (finalIntent, error) {
	var intent finalIntent
	if err := readState(setup.root, "final.json", &intent); err != nil {
		return intent, err
	}
	var https panelHTTPSIntent
	if readState(setup.root, "panel-https.json", &https) != nil || intent.HTTPS != https || !panelProofMatches(https.Proof, setup.imported, setup.enrolled) || https.OperationID != setup.imported.OperationID || https.Pins != setup.imported.Pins || https.Origin == "" || https.NodeID == "" || intent.OperationID != setup.imported.OperationID || intent.Pins != setup.imported.Pins || intent.EnrollmentSystem != setup.imported.Build.EnrollmentSystem || intent.FinalSystem != setup.imported.Build.FinalSystem || intent.FinalSystem == intent.EnrollmentSystem || intent.Operator != setup.input.Operator.Name || !canonicalOperation(intent.LaunchID) || !validFinalChallenge(intent.Challenge) || intent.RecoveryDeadline <= 0 || !intent.IndependentRecoveryVerified {
		return intent, ErrFinal
	}
	return intent, nil
}
func finalPhase(root *os.Root, intent finalIntent) (finalReceipt, error) {
	var receipt finalReceipt
	if readState(root, "final-receipt.json", &receipt) != nil || receipt.OperationID != intent.OperationID {
		return receipt, ErrFinal
	}
	switch receipt.Phase {
	case "starting", "preparing", "profile_updating", "switching", "awaiting_private", "recovering", "restored", "needs_attention":
		if receipt.ConfirmedAt != 0 {
			return receipt, ErrFinal
		}
	case "final_active":
		if receipt.ConfirmedAt <= 0 || receipt.ConfirmedAt >= intent.RecoveryDeadline {
			return receipt, ErrFinal
		}
	default:
		return receipt, ErrFinal
	}
	return receipt, nil
}
func writeFinalPhase(ctx context.Context, root *os.Root, intent finalIntent, phase string) error {
	return writeState(ctx, root, "final-receipt.json", finalReceipt{OperationID: intent.OperationID, Phase: phase}, true)
}
func finalEvidenceAbsent(root *os.Root) bool {
	for _, name := range []string{"final.json", "final-receipt.json"} {
		if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			return false
		}
	}
	return true
}
func finalResult(setup *finalSetup, intent finalIntent, status string, links enrollmentLinks) FinalResult {
	deadline := ""
	if intent.RecoveryDeadline > 0 {
		deadline = time.Unix(intent.RecoveryDeadline, 0).UTC().Format(time.RFC3339)
	}
	return FinalResult{Status: status, OperationID: setup.imported.OperationID, InstallationID: setup.imported.Build.InstallationID, ServerID: setup.imported.Build.ServerID, InputSHA256: setup.imported.Build.InputSHA256, EnrollmentSystem: setup.imported.Build.EnrollmentSystem, FinalSystem: setup.imported.Build.FinalSystem, CurrentSystem: links.current, ProfileSystem: links.profile, BootedSystem: links.booted, Origin: intent.HTTPS.Origin, Challenge: intent.Challenge, RecoveryDeadline: deadline}
}
