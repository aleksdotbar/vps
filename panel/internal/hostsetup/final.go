package hostsetup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"time"

	"github.com/google/uuid"
)

// ActivateFinal is reached through a fresh operator-owned private SSH/sudo
// connection. Independent recovery is an explicit trusted operator attestation,
// not a fact inferred from the target's own Tailscale or HTTPS observations.
func ActivateFinal(ctx context.Context, operation, buildSHA string, admission []byte) (FinalResult, error) {
	if !admitFinal(admission) {
		return FinalResult{}, ErrFinal
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	setup, err := openFinalSetup(ctx, operation, adoptionPins{buildSHA})
	if err != nil {
		return FinalResult{}, err
	}
	defer setup.close()
	if confirmFinalOperator(setup.input.Operator.Name) != nil {
		return FinalResult{}, ErrFinal
	}
	existing, err := readFinalIntent(setup)
	if err == nil {
		return observeFinal(ctx, setup, existing)
	}
	if !errors.Is(err, os.ErrNotExist) || !finalEvidenceAbsent(setup.root) {
		return FinalResult{}, ErrFinal
	}
	links, err := readEnrollmentLinks()
	if err != nil || !enrollmentAt(setup.enrolled, links, setup.enrolled.EnrollmentSystem) {
		return FinalResult{}, ErrFinal
	}
	var https panelHTTPSIntent
	if readState(setup.root, "panel-https.json", &https) != nil || https.OperationID != operation || https.Pins != setup.imported.Pins || !panelProofMatches(https.Proof, setup.imported, setup.enrolled) {
		return FinalResult{}, ErrFinal
	}
	selected, err := panelNativeSelf(ctx)
	if err != nil || selected.origin != https.Origin || selected.nodeID != https.NodeID {
		return FinalResult{}, ErrFinal
	}
	if verifyFinalPanel(ctx, setup, selected, setup.enrolled.EnrollmentSystem, false) != nil {
		return FinalResult{}, ErrFinal
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return FinalResult{}, ErrFinal
	}
	intent := finalIntent{OperationID: operation, Pins: setup.imported.Pins, EnrollmentSystem: setup.imported.Build.EnrollmentSystem, FinalSystem: setup.imported.Build.FinalSystem, Operator: setup.input.Operator.Name, HTTPS: https, Challenge: hex.EncodeToString(nonce), LaunchID: uuid.NewString(), RecoveryDeadline: time.Now().Add(12 * time.Minute).Unix(), IndependentRecoveryVerified: true}
	if !finalUnitsAbsent(ctx, intent) {
		return FinalResult{}, ErrFinal
	}
	tools := targetImportTools()
	tools.nixStore = intent.EnrollmentSystem + "/sw/bin/nix-store"
	if complete, err := observeRootedImport(ctx, tools, setup.imported.Build); err != nil || !complete {
		return FinalResult{}, ErrUnresolved
	}
	if writeState(ctx, setup.root, "final.json", intent, false) != nil || writeFinalPhase(ctx, setup.root, intent, "starting") != nil {
		return FinalResult{}, ErrUnresolved
	}
	// Recovery is armed before the runner. Neither an uncertain launch nor a
	// disconnected caller permits replaying the protected starting operation.
	if launchFinalUnit(ctx, intent, true) != nil {
		return FinalResult{}, ErrUnresolved
	}
	if setup.lock.Close() != nil {
		return FinalResult{}, ErrUnresolved
	}
	if launchFinalUnit(ctx, intent, false) != nil {
		return FinalResult{}, ErrUnresolved
	}
	return finalResult(setup, intent, "starting", links), nil
}

func FinalStatus(ctx context.Context, operation, buildSHA string) (FinalResult, error) {
	setup, err := openFinalSetup(ctx, operation, adoptionPins{buildSHA})
	if err != nil {
		return FinalResult{}, err
	}
	defer setup.close()
	intent, err := readFinalIntent(setup)
	if errors.Is(err, os.ErrNotExist) {
		if !finalEvidenceAbsent(setup.root) {
			return FinalResult{}, ErrFinal
		}
		links, err := readEnrollmentLinks()
		if err != nil {
			return FinalResult{}, err
		}
		var https panelHTTPSIntent
		if readState(setup.root, "panel-https.json", &https) != nil || https.OperationID != operation || https.Pins != setup.imported.Pins || !panelProofMatches(https.Proof, setup.imported, setup.enrolled) {
			return FinalResult{}, ErrFinal
		}
		return finalResult(setup, finalIntent{HTTPS: https}, "not_started", links), nil
	}
	if err != nil {
		return FinalResult{}, err
	}
	return observeFinal(ctx, setup, intent)
}
func observeFinal(ctx context.Context, setup *finalSetup, intent finalIntent) (FinalResult, error) {
	links, err := readEnrollmentLinks()
	if err != nil {
		return FinalResult{}, err
	}
	receipt, phaseErr := finalPhase(setup.root, intent)
	status := "needs_attention"
	if phaseErr == nil && finalOwnsObservedLinks(intent, setup.enrolled, links) {
		switch receipt.Phase {
		case "final_active":
			if finalAt(intent, setup.enrolled, links, intent.FinalSystem) {
				status = "final_active"
			}
		case "restored":
			if finalAt(intent, setup.enrolled, links, intent.EnrollmentSystem) {
				status = "restored"
			}
		default:
			unit, err := ownedFinalUnit(ctx, intent, false)
			if err == nil {
				if !unit.terminal() {
					status = "activating"
				} else if receipt.Phase == "awaiting_private" && unit.result == "success" && unit.exit == "0" && finalAt(intent, setup.enrolled, links, intent.FinalSystem) && time.Now().Unix() < intent.RecoveryDeadline {
					status = "awaiting_private"
				}
			}
			recovery, err := ownedFinalUnit(ctx, intent, true)
			if err == nil && !recovery.terminal() {
				status = "recovering"
			}
		}
	}
	return finalResult(setup, intent, status, links), nil
}

func ConfirmFinal(ctx context.Context, operation, buildSHA string, proof []byte) (FinalResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	setup, err := openFinalSetup(ctx, operation, adoptionPins{buildSHA})
	if err != nil {
		return FinalResult{}, err
	}
	defer setup.close()
	intent, err := readFinalIntent(setup)
	if err != nil || confirmFinalOperator(setup.input.Operator.Name) != nil {
		return FinalResult{}, ErrFinal
	}
	links, err := readEnrollmentLinks()
	if err != nil || !finalAt(intent, setup.enrolled, links, intent.FinalSystem) || !verifyFinalProbe(proof, setup, intent, links) {
		return FinalResult{}, ErrFinal
	}
	observed, err := observeFinal(ctx, setup, intent)
	if err != nil {
		return FinalResult{}, err
	}
	if observed.Status == "final_active" {
		return observed, nil
	}
	if observed.Status != "awaiting_private" || time.Now().Unix() >= intent.RecoveryDeadline {
		return FinalResult{}, ErrFinal
	}
	selected, err := panelNativeSelf(ctx)
	if err != nil || selected.origin != intent.HTTPS.Origin || selected.nodeID != intent.HTTPS.NodeID || verifyFinalPanel(ctx, setup, selected, intent.FinalSystem, true) != nil {
		return FinalResult{}, ErrFinal
	}
	if verifyFinalHost(ctx, setup, intent) != nil {
		return FinalResult{}, ErrFinal
	}
	confirmedAt := time.Now().Unix()
	if confirmedAt >= intent.RecoveryDeadline {
		return FinalResult{}, ErrFinal
	}
	identity := hostIdentity{InstallationID: setup.imported.Build.InstallationID, ServerID: setup.imported.Build.ServerID, Role: setup.imported.Build.Role}
	receipt := finalReceipt{OperationID: operation, Phase: "final_active", ConfirmedAt: confirmedAt, Identity: &identity, FinalSystem: intent.FinalSystem, BootedSystem: links.booted, RecoveryDeadline: intent.RecoveryDeadline}
	if writeState(ctx, setup.root, "final-receipt.json", receipt, true) != nil {
		return FinalResult{}, ErrUnresolved
	}
	if stopFinalGuard(ctx, intent) != nil {
		return FinalResult{}, ErrUnresolved
	}
	observed.Status = "final_active"
	return observed, nil
}
