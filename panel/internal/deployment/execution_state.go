package deployment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/privatefile"
	"github.com/aleksdotbar/vps/panel/internal/routes"
	"golang.org/x/sys/unix"
)

const recoveryReserve = 5 * time.Minute

type execution struct {
	Claim           claim                     `json:"claim"`
	LaunchID        string                    `json:"launch_id"`
	BootID          string                    `json:"boot_id"`
	InvocationID    string                    `json:"invocation_id,omitempty"`
	RecoveryStarted bool                      `json:"recovery_started"`
	Phase           string                    `json:"phase"`
	Reason          string                    `json:"reason,omitempty"`
	Previous        *releaseReference         `json:"previous,omitempty"`
	Preparation     routes.RuntimePreparation `json:"preparation"`
	RouteReceipt    routes.RuntimeReceipt     `json:"route_receipt"`
	Diagnostics     []ServiceDiagnostic       `json:"diagnostics,omitempty"`
}

type releaseReference struct {
	Identity       Identity `json:"identity"`
	ManifestSHA256 string   `json:"manifest_sha256"`
}

type releaseMaterial struct {
	Stage         staged            `json:"stage"`
	RuntimeConfig json.RawMessage   `json:"runtime_config"`
	Origins       []routes.Origin   `json:"origins"`
	ImageIDs      map[string]string `json:"image_ids"`
}

func replaceRecord(ctx context.Context, root *os.Root, name string, expected, next any) error {
	actual, err := readFile(root, name)
	if err != nil {
		return ErrState
	}
	defer clear(actual)
	wanted, err := json.Marshal(expected)
	defer clear(wanted)
	if err != nil || !bytes.Equal(actual, wanted) {
		return ErrState
	}
	encoded, err := json.Marshal(next)
	defer clear(encoded)
	if err != nil {
		return ErrState
	}
	temporary := ".next"
	if leftover, err := readFile(root, temporary); err == nil {
		clear(leftover)
		if root.Remove(temporary) != nil {
			return ErrState
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrState
	}
	if publish(ctx, root, temporary, encoded) != nil {
		return ErrState
	}
	defer func() { _ = root.Remove(temporary) }()
	if ctx.Err() != nil || root.Rename(temporary, name) != nil || syncDirectory(root) != nil {
		return ErrState
	}
	return nil
}

func putRecord(ctx context.Context, root *os.Root, name string, value any) error {
	data, err := json.Marshal(value)
	defer clear(data)
	if err != nil {
		return ErrState
	}
	return publish(ctx, root, name, data)
}

func executionLock(environment *os.Root) (*os.File, error) {
	lock, err := privatefile.OpenNoFollow(environment, "effect.lock", os.O_RDWR, 0)
	if err != nil {
		return nil, ErrState
	}
	info, err := lock.Stat()
	if err != nil || !protected(info, false) {
		_ = lock.Close()
		return nil, ErrState
	}
	if unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		_ = lock.Close()
		return nil, ErrBusy
	}
	return lock, nil
}

func readExecution(environment *os.Root, expected claim) (execution, error) {
	var value execution
	directory, err := operationRoot(environment, expected.Identity.OperationID, false)
	if err != nil {
		return value, err
	}
	defer func() { _ = directory.Close() }()
	if err := readRecord(directory, "execution.json", &value); err != nil {
		return value, err
	}
	invocationAdmitted := validNativeInvocation(value.InvocationID) || value.InvocationID == "" && (value.Phase == "starting" || value.Phase == "failed" || value.Phase == "needs_attention")
	if value.Claim != expected || !validID(value.LaunchID) || !validID(value.BootID) || !executionPhase(value.Phase) || !safeExecutionReason(value.Reason) || len(value.Preparation.Origins) > 32 || !invocationAdmitted || !validDiagnostics(value.Diagnostics) {
		return value, ErrState
	}
	if (value.Previous == nil) != (expected.Identity.ExpectedCurrentReleaseID == "") {
		return value, ErrState
	}
	if value.Previous != nil && (ValidateIdentity(value.Previous.Identity) != nil || !validHash(value.Previous.ManifestSHA256) || value.Previous.Identity.EnvironmentID != expected.Identity.EnvironmentID || value.Previous.Identity.InstallationID != expected.Identity.InstallationID || value.Previous.Identity.ServerID != expected.Identity.ServerID || value.Previous.Identity.ReleaseID != expected.Identity.ExpectedCurrentReleaseID) {
		return value, ErrState
	}
	return value, nil
}

func writePhase(ctx context.Context, environment *os.Root, value *execution, phase, reason string) error {
	if !executionPhase(phase) {
		return ErrState
	}
	next := *value
	next.Phase, next.Reason = phase, reason
	if replaceExecution(ctx, environment, *value, next) != nil {
		return ErrState
	}
	*value = next
	slog.Info("deployment phase", "operation", value.Claim.Identity.OperationID, "phase", phase, "reason", reason)
	return nil
}

func readCurrent(environment *os.Root) (*releaseReference, error) {
	var value releaseReference
	err := readRecord(environment, "current.json", &value)
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	if err != nil || ValidateIdentity(value.Identity) != nil || !validHash(value.ManifestSHA256) {
		return nil, ErrState
	}
	return &value, nil
}

type preparedMaterial struct {
	RuntimeConfig json.RawMessage   `json:"runtime_config"`
	Origins       []routes.Origin   `json:"origins"`
	ImageIDs      map[string]string `json:"image_ids"`
}

func operationRoot(environment *os.Root, operationID string, create bool) (*os.Root, error) {
	if !validID(operationID) {
		return nil, ErrState
	}
	operations, err := child(environment, "operations", create)
	if err != nil {
		return nil, err
	}
	defer func() { _ = operations.Close() }()
	return child(operations, operationID, create)
}

func putExecution(ctx context.Context, environment *os.Root, value execution) error {
	directory, err := operationRoot(environment, value.Claim.Identity.OperationID, false)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return putRecord(ctx, directory, "execution.json", value)
}

func replaceExecution(ctx context.Context, environment *os.Root, previous, next execution) error {
	if previous.Claim != next.Claim {
		return ErrState
	}
	directory, err := operationRoot(environment, previous.Claim.Identity.OperationID, false)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return replaceRecord(ctx, directory, "execution.json", previous, next)
}

func material(environment *os.Root, reference releaseReference) (releaseMaterial, error) {
	var value releaseMaterial
	stage, err := readStage(environment, claim{Identity: reference.Identity, ManifestSHA256: reference.ManifestSHA256})
	if err != nil {
		return value, err
	}
	value.Stage = stage
	directory, err := operationRoot(environment, reference.Identity.OperationID, false)
	if err != nil {
		clear(stage.ApplicationConfig)
		return value, err
	}
	defer func() { _ = directory.Close() }()
	var prepared preparedMaterial
	if readRecord(directory, "material.json", &prepared) != nil || len(prepared.RuntimeConfig) > MaxApplicationBytes || !json.Valid(prepared.RuntimeConfig) || len(prepared.Origins) > 32 || len(prepared.ImageIDs) != len(stage.Manifest.Artifact.Services) {
		clear(stage.ApplicationConfig)
		return releaseMaterial{}, ErrState
	}
	value.RuntimeConfig, value.Origins, value.ImageIDs = prepared.RuntimeConfig, prepared.Origins, prepared.ImageIDs
	for service := range stage.Manifest.Artifact.Services {
		if !strings.HasPrefix(value.ImageIDs[service], "sha256:") || !validHash(strings.TrimPrefix(value.ImageIDs[service], "sha256:")) {
			clear(stage.ApplicationConfig)
			return releaseMaterial{}, ErrState
		}
	}
	return value, nil
}

func saveMaterial(ctx context.Context, environment *os.Root, value releaseMaterial) error {
	directory, err := operationRoot(environment, value.Stage.Claim.Identity.OperationID, false)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return putRecord(ctx, directory, "material.json", preparedMaterial{value.RuntimeConfig, value.Origins, value.ImageIDs})
}

func commitCurrent(ctx context.Context, environment *os.Root, value execution) error {
	current, err := readCurrent(environment)
	if err != nil && !errors.Is(err, os.ErrNotExist) || !sameReference(current, value.Previous) {
		return ErrState
	}
	next := releaseReference{value.Claim.Identity, value.Claim.ManifestSHA256}
	if current == nil {
		return putRecord(ctx, environment, "current.json", next)
	}
	return replaceRecord(ctx, environment, "current.json", current, next)
}

func sameReference(left, right *releaseReference) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func admitNextStage(ctx context.Context, environment *os.Root, previous claim, expected claim, next staged) error {
	value, err := readExecution(environment, previous)
	if err != nil || !terminalPhase(value.Phase) || value.Phase == "needs_attention" {
		return ErrBusy
	}
	current, err := readCurrent(environment)
	if err != nil && !errors.Is(err, os.ErrNotExist) || current == nil && expected.Identity.ExpectedCurrentReleaseID != "" || current != nil && !expectedCurrent(*current, expected.Identity) || previous.Identity.InstallationID != expected.Identity.InstallationID || previous.Identity.ServerID != expected.Identity.ServerID {
		return ErrBusy
	}
	keep := map[string]bool{previous.Identity.OperationID: true, expected.Identity.OperationID: true}
	if current != nil {
		keep[current.Identity.OperationID] = true
	}
	if value.Previous != nil {
		keep[value.Previous.Identity.OperationID] = true
	}
	if pruneOperations(ctx, environment, expected.Identity, keep) != nil || retainStage(ctx, environment, next) != nil {
		return ErrState
	}
	return replaceRecord(ctx, environment, "active.json", previous, expected)
}

func pruneOperations(ctx context.Context, environment *os.Root, owner Identity, keep map[string]bool) error {
	directory, err := child(environment, "operations", false)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.Open(".")
	if err != nil {
		return ErrState
	}
	defer func() { _ = entries.Close() }()
	for {
		batch, readErr := entries.ReadDir(64)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return ErrState
		}
		for _, entry := range batch {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if keep[entry.Name()] {
				continue
			}
			if !validID(entry.Name()) {
				return ErrState
			}
			operation, err := child(directory, entry.Name(), false)
			if err != nil {
				return err
			}
			var value execution
			stateErr := readRecord(operation, "execution.json", &value)
			admitted := directoryEntries(operation, "stage.json", "execution.json", "material.json", "confirmation.json", ".next") == nil
			if stateErr == nil {
				admitted = admitted && value.Claim.Identity.OperationID == entry.Name() && value.Claim.Identity.InstallationID == owner.InstallationID && value.Claim.Identity.ServerID == owner.ServerID && value.Claim.Identity.EnvironmentID == owner.EnvironmentID && terminalPhase(value.Phase) && value.Phase != "needs_attention"
			}
			_ = operation.Close()
			if !admitted || stateErr != nil && !errors.Is(stateErr, os.ErrNotExist) {
				continue
			}
			if directory.RemoveAll(entry.Name()) != nil {
				return ErrState
			}
		}
		if errors.Is(readErr, io.EOF) {
			return syncDirectory(directory)
		}
	}
}

func expectedCurrent(reference releaseReference, identity Identity) bool {
	return reference.Identity.ReleaseID == identity.ExpectedCurrentReleaseID && reference.Identity.InstallationID == identity.InstallationID && reference.Identity.ServerID == identity.ServerID && reference.Identity.EnvironmentID == identity.EnvironmentID
}

func readConfirmation(environment *os.Root, expected claim) (bool, error) {
	directory, err := operationRoot(environment, expected.Identity.OperationID, false)
	if err != nil {
		return false, err
	}
	defer func() { _ = directory.Close() }()
	var value claim
	err = readRecord(directory, "confirmation.json", &value)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || value != expected {
		return false, ErrState
	}
	return true, nil
}

func saveConfirmation(ctx context.Context, environment *os.Root, expected claim) error {
	directory, err := operationRoot(environment, expected.Identity.OperationID, false)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	var previous claim
	err = readRecord(directory, "confirmation.json", &previous)
	if err == nil {
		if previous != expected {
			return ErrState
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return ErrState
	}
	return putRecord(ctx, directory, "confirmation.json", expected)
}
