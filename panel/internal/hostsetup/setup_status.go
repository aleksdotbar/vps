package hostsetup

import (
	"context"
	"errors"
	"github.com/aleksdotbar/vps/panel/internal/foundation"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/privatefile"
)

// SetupStatus is an observation of existing receipts, never a second workflow
// or permission to repeat an uncertain effect. Null stages have proven absence.
type SetupStatus struct {
	OperationID string            `json:"operation_id"`
	ImportPhase string            `json:"import_phase"`
	Binding     *SetupBinding     `json:"binding"`
	Enrollment  *EnrollmentResult `json:"enrollment"`
	HTTPS       *SetupHTTPS       `json:"https"`
	Final       *FinalResult      `json:"final"`
}
type SetupBinding struct {
	InstallationID string `json:"installation_id"`
	ServerID       string `json:"server_id"`
	InputSHA256    string `json:"input_sha256"`
}
type SetupHTTPS struct {
	Status string `json:"status"`
	Origin string `json:"origin"`
}

func ObserveSetup(ctx context.Context, operation, buildSHA string) (SetupStatus, error) {
	pins := adoptionPins{buildSHA}
	result := SetupStatus{OperationID: operation, ImportPhase: "not_started"}
	if os.Geteuid() != 0 {
		return result, ErrPrivilege
	}
	if !canonicalOperation(operation) || !pins.valid() {
		return result, ErrIdentity
	}
	if _, err := os.Lstat(hostStateDirectory); errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	root, err := enrollmentStateRoot()
	if err != nil {
		return result, err
	}
	defer func() { _ = root.Close() }()
	return observeSetupAt(ctx, root, operation, pins, targetImportTools())
}
func observeSetupAt(parent context.Context, root *os.Root, operation string, pins adoptionPins, tools importTools) (SetupStatus, error) {
	result := SetupStatus{OperationID: operation, ImportPhase: "not_started"}
	ctx, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	directory, err := root.Open(".")
	if err != nil {
		return result, ErrIdentity
	}
	names, readErr := directory.Readdirnames(2)
	_ = directory.Close()
	if len(names) == 0 && (readErr == nil || errors.Is(readErr, io.EOF)) {
		return result, nil
	}
	lock, err := privatefile.OpenNoFollow(root, "setup.lock", os.O_RDWR, 0)
	if err != nil {
		return result, ErrUnresolved
	}
	defer func() { _ = lock.Close() }()
	info, err := lock.Stat()
	if err != nil || info == nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !sameSetupOwner(info) {
		return result, ErrIdentity
	}
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return result, ErrBusy
	}
	imported, err := readImportIntent(root, operation, pins)
	if errors.Is(err, os.ErrNotExist) {
		directory, err := root.Open(".")
		if err != nil {
			return result, ErrIdentity
		}
		names, err := directory.Readdirnames(3)
		_ = directory.Close()
		if (err == nil || errors.Is(err, io.EOF)) && len(names) == 1 && names[0] == "setup.lock" {
			return result, nil
		}
		return result, ErrUnresolved
	}
	if err != nil {
		return result, err
	}
	var receipt importReceipt
	if readState(root, "receipt.json", &receipt) != nil || receipt.OperationID != operation {
		return result, ErrUnresolved
	}
	if receipt.Phase != "imported" {
		return result, ErrUnresolved
	}
	result.ImportPhase = receipt.Phase
	result.Binding = &SetupBinding{imported.Build.InstallationID, imported.Build.ServerID, imported.Build.InputSHA256}
	if tools.store == "local" {
		tools.nixStore = imported.Build.EnrollmentSystem + "/sw/bin/nix-store"
	}
	// Shared native GC exclusion preserves the snapshot; status never creates
	// roots, repairs receipts, imports paths, or opens the application database.
	gc, err := privatefile.OpenPrivate(gcLockPath(tools), os.O_RDWR)
	if err != nil {
		return result, ErrUnresolved
	}
	defer func() { _ = gc.Close() }()
	if syscall.Flock(int(gc.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) != nil || !observedImport(ctx, tools, imported.Build) {
		return result, ErrUnresolved
	}
	enrolled, err := readEnrollmentIntent(root, imported)
	if errors.Is(err, os.ErrNotExist) {
		if !enrollmentEvidenceAbsent(root) {
			return result, ErrUnresolved
		}
		return result, nil
	}
	if err != nil {
		return result, err
	}
	observation, err := observeEnrollment(ctx, root, imported, enrolled)
	if err != nil {
		return result, err
	}
	result.Enrollment = &observation
	if imported.Build.Role != foundation.Panel {
		return result, nil
	}
	if _, finalErr := root.Lstat("final.json"); errors.Is(finalErr, os.ErrNotExist) && observation.Status != "enrolled" {
		return result, nil
	} else if finalErr != nil && !errors.Is(finalErr, os.ErrNotExist) {
		return result, ErrUnresolved
	}
	var https panelHTTPSIntent
	err = readState(root, "panel-https.json", &https)
	if errors.Is(err, os.ErrNotExist) {
		if !finalEvidenceAbsent(root) {
			return result, ErrUnresolved
		}
		unit, err := panelService(ctx, imported.Build.EnrollmentSystem)
		if err != nil || unit.active != "inactive" || !panelConfigAbsent() {
			return result, ErrUnresolved
		}
		result.HTTPS = &SetupHTTPS{Status: "not_started"}
		return result, nil
	}
	if err != nil || https.OperationID != operation || https.Pins != pins || !panelProofMatches(https.Proof, imported, enrolled) {
		return result, ErrIdentity
	}
	result.HTTPS = &SetupHTTPS{Status: "retained", Origin: https.Origin}
	input, err := retainedInspectionInput(ctx, root, imported)
	if err != nil {
		return result, err
	}
	setup := &finalSetup{root: root, lock: lock, imported: imported, enrolled: enrolled, input: input}
	final, err := readFinalIntent(setup)
	if errors.Is(err, os.ErrNotExist) {
		if !finalEvidenceAbsent(root) {
			return result, ErrUnresolved
		}
		return result, nil
	}
	if err != nil {
		return result, err
	}
	finalObservation, err := observeFinal(ctx, setup, final)
	if err != nil {
		return result, err
	}
	result.Final = &finalObservation
	return result, nil
}

func sameSetupOwner(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int64(stat.Uid) == int64(os.Geteuid())
}
