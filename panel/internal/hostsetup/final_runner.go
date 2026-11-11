package hostsetup

import (
	"context"
	"os"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/foundation"
)

func retainedFinalSetup(ctx context.Context, operation string) (*finalSetup, error) {
	root, err := enrollmentStateRoot()
	if err != nil {
		return nil, err
	}
	var imported importIntent
	err = readState(root, "intent.json", &imported)
	if err != nil || !canonicalOperation(operation) || !imported.Pins.valid() {
		_ = root.Close()
		return nil, ErrIdentity
	}
	lock, err := waitStateLock(ctx, root)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return loadFinalSetup(ctx, root, lock, operation, imported.Pins)
}
func RunFinal(ctx context.Context, operation string, recovery bool) error {
	ctx, cancel := context.WithTimeout(ctx, 9*time.Minute)
	defer cancel()
	setup, err := retainedFinalSetup(ctx, operation)
	if err != nil {
		return err
	}
	defer setup.close()
	intent, err := readFinalIntent(setup)
	if err != nil {
		return err
	}
	unit, err := ownedFinalUnit(ctx, intent, recovery)
	if err != nil || unit.terminal() {
		return ErrFinal
	}
	if recovery {
		return recoverFinal(ctx, setup, intent)
	}
	receipt, err := finalPhase(setup.root, intent)
	if err != nil || !finalMayStart(intent, receipt.Phase, time.Now()) {
		return ErrFinal
	}
	links, err := readEnrollmentLinks()
	if err != nil || !finalAt(intent, setup.enrolled, links, intent.EnrollmentSystem) {
		return ErrFinal
	}
	if writeFinalPhase(ctx, setup.root, intent, "preparing") != nil {
		return ErrUnresolved
	}
	if verifyFinalHost(ctx, setup, intent) != nil {
		return ErrUnresolved
	}
	selected, err := panelNativeSelf(ctx)
	if err != nil || selected.origin != intent.HTTPS.Origin || selected.nodeID != intent.HTTPS.NodeID || verifyFinalPanel(ctx, setup, selected, intent.EnrollmentSystem, false) != nil {
		return ErrUnresolved
	}
	if setup.lock.Close() != nil {
		return ErrUnresolved
	}
	if nativeFinalSwitch(ctx, setup, intent, intent.FinalSystem) != nil {
		return ErrUnresolved
	}
	setup.lock, err = waitStateLock(ctx, setup.root)
	if err != nil {
		return err
	}
	links, err = readEnrollmentLinks()
	if err != nil || !finalAt(intent, setup.enrolled, links, intent.FinalSystem) {
		return ErrUnresolved
	}
	if verifyFinalHost(ctx, setup, intent) != nil {
		return ErrUnresolved
	}
	if awaitPanelHTTPS(ctx, intent.FinalSystem, selected) != nil || verifyFinalPanel(ctx, setup, selected, intent.FinalSystem, true) != nil {
		return ErrUnresolved
	}
	return writeFinalPhase(ctx, setup.root, intent, "awaiting_private")
}
func recoverFinal(ctx context.Context, setup *finalSetup, intent finalIntent) error {
	main, err := ownedFinalUnit(ctx, intent, false)
	if err != nil || !main.terminal() {
		return ErrUnresolved
	}
	receipt, err := finalPhase(setup.root, intent)
	if err != nil {
		return err
	}
	if receipt.Phase == "final_active" || receipt.Phase == "restored" {
		return nil
	}
	if receipt.Phase == "recovering" {
		return ErrUnresolved
	}
	links, err := readEnrollmentLinks()
	if err != nil || !finalMayRecover(intent, setup.enrolled, receipt, links) {
		return ErrUnresolved
	}
	// A single observed owned attempt may restore enrollment. An interrupted
	// restoration, reboot/missing unit, or third generation never permits replay.
	if writeFinalPhase(ctx, setup.root, intent, "recovering") != nil {
		return ErrUnresolved
	}
	if !finalAt(intent, setup.enrolled, links, intent.EnrollmentSystem) {
		if setup.lock.Close() != nil {
			return ErrUnresolved
		}
		if nativeFinalSwitch(ctx, setup, intent, intent.EnrollmentSystem) != nil {
			return ErrUnresolved
		}
		setup.lock, err = waitStateLock(ctx, setup.root)
		if err != nil {
			return err
		}
	}
	links, err = readEnrollmentLinks()
	if err != nil || !finalAt(intent, setup.enrolled, links, intent.EnrollmentSystem) {
		return ErrUnresolved
	}
	if verifyFinalHost(ctx, setup, intent) != nil {
		return ErrUnresolved
	}
	selected, err := panelNativeSelf(ctx)
	if err != nil || selected.origin != intent.HTTPS.Origin || selected.nodeID != intent.HTTPS.NodeID || awaitPanelHTTPS(ctx, intent.EnrollmentSystem, selected) != nil {
		return ErrUnresolved
	}
	// Access bundle and encrypted panel state are retained; restoring enrollment
	// reopens only its reviewed bootstrap policy, never a manufactured new key.
	return writeFinalPhase(ctx, setup.root, intent, "restored")
}

// FinalAuthority holds setup exclusion for a concrete privileged mutation.
// Callers must Close it after their operation, including failures/cancellation.
// It establishes settled native identity only, never application readiness.
type FinalAuthority struct {
	InstallationID string
	ServerID       string
	FinalSystem    string
	root           *os.Root
	lock           *os.File
}

func (authority *FinalAuthority) Close() error {
	if authority == nil || authority.root == nil {
		return nil
	}
	lockErr := authority.lock.Close()
	rootErr := authority.root.Close()
	authority.root = nil
	if lockErr != nil || rootErr != nil {
		return ErrFinal
	}
	return nil
}

func readSettledFinal(root *os.Root, links enrollmentLinks) (finalReceipt, error) {
	var receipt finalReceipt
	if readState(root, "final-receipt.json", &receipt) != nil || !canonicalOperation(receipt.OperationID) || receipt.Phase != "final_active" || receipt.ConfirmedAt <= 0 || receipt.ConfirmedAt >= receipt.RecoveryDeadline || receipt.Identity == nil {
		return receipt, ErrFinal
	}
	identity := receipt.Identity
	if !canonicalOperation(identity.InstallationID) || !canonicalOperation(identity.ServerID) || identity.Role != foundation.Panel || !validEnrollmentSystem(receipt.FinalSystem) || links.current != receipt.FinalSystem || links.profile != receipt.FinalSystem || (links.booted != receipt.FinalSystem && links.booted != receipt.BootedSystem) {
		return receipt, ErrFinal
	}
	return receipt, nil
}

func AcquireSettledFinal(ctx context.Context) (*FinalAuthority, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
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
	authority := &FinalAuthority{root: root, lock: lock}
	links, err := readEnrollmentLinks()
	if err != nil {
		_ = authority.Close()
		return nil, ErrFinal
	}
	// Confirmation already verified this exact immutable generation. Runtime
	// retains setup exclusion without reopening installation history.
	receipt, err := readSettledFinal(root, links)
	if err != nil {
		_ = authority.Close()
		return nil, err
	}
	authority.InstallationID = receipt.Identity.InstallationID
	authority.ServerID = receipt.Identity.ServerID
	authority.FinalSystem = receipt.FinalSystem
	return authority, nil
}
