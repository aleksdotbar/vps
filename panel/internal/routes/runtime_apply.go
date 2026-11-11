package routes

import (
	"context"
	"errors"
	"math"
	"os"
	"slices"
)

func receiptFor(operation RuntimeOperation, ledger runtimeLedger, phase RuntimePhase) RuntimeReceipt {
	active := operation.Snapshot.ReleaseID
	if phase == RuntimeApplying {
		active = ""
	}
	if phase == RuntimeRestored {
		active = operation.ExpectedCurrentReleaseID
	}
	if phase == RuntimeNeutral {
		active = ""
		ledger.Revision = 0
		ledger.ConfigSHA256 = ""
	}
	return RuntimeReceipt{OperationID: operation.Snapshot.OperationID, ReleaseID: operation.Snapshot.ReleaseID, EnvironmentID: operation.Snapshot.EnvironmentID, RouteSnapshotSHA256: operation.Snapshot.SHA256, ActiveReleaseID: active, Revision: ledger.Revision, ConfigSHA256: ledger.ConfigSHA256, Phase: phase}
}

func findRuntimeAllocation(ledger runtimeLedger, key runtimeKey) (runtimeAllocation, error) {
	for _, allocation := range ledger.Allocations {
		if allocation.Key == key {
			return allocation, nil
		}
	}
	return runtimeAllocation{}, ErrRuntimeState
}

// ApplyRuntime is called only after the runner verifies exact native candidate
// publications and health. It changes one environment and preserves the latest
// contributions from every other environment under the server route lock.
func ApplyRuntime(ctx context.Context, operation RuntimeOperation, expectedRevision uint64, expectedConfigSHA256, finalSystem string) (RuntimeReceipt, error) {
	ctx, cancel, err := runtimeContext(ctx, operation)
	if err != nil {
		return RuntimeReceipt{}, err
	}
	defer cancel()
	r, err := openRuntime(operation, finalSystem, false)
	if errors.Is(err, os.ErrNotExist) && len(operation.Snapshot.Bindings) == 0 {
		return receiptFor(operation, runtimeLedger{}, RuntimeNeutral), nil
	}
	if err != nil {
		return RuntimeReceipt{}, err
	}
	defer r.close()
	if r.ledger.SchemaVersion == 0 {
		return receiptFor(operation, r.ledger, RuntimeNeutral), nil
	}
	key := operationKey(operation)
	if r.ledger.Pending != nil {
		if r.ledger.Pending.Key != key || r.ledger.Pending.Restore {
			return RuntimeReceipt{}, ErrRuntimeBusy
		}
		return advanceRuntimeMutation(ctx, r, operation, true)
	}
	index := environmentIndex(&r.ledger, key.EnvironmentID)
	if index >= 0 && r.ledger.Environments[index].Current != nil && r.ledger.Environments[index].Current.Key == key {
		return observeRuntime(ctx, r, operation)
	}
	pending, configuration, err := planRuntimeApply(r.ledger, operation, expectedRevision, expectedConfigSHA256)
	if err != nil {
		return RuntimeReceipt{}, err
	}
	if err := ValidateTunnelConfiguration(ctx, r.connector, configuration, nativeTunnelTools(finalSystem).cloudflared); err != nil {
		return RuntimeReceipt{}, err
	}
	current, err := runtimeConfiguration(r.ledger, nil)
	if err != nil {
		return RuntimeReceipt{}, err
	}
	observed, err := ObserveTunnelRuntime(ctx, finalSystem, r.connector, r.credentials, current)
	if err != nil || !observed.Ready {
		return RuntimeReceipt{}, ErrTunnelRuntime
	}
	r.ledger.Pending = &pending
	if err := saveRuntimeLedger(ctx, r.state, r.ledger); err != nil {
		return RuntimeReceipt{}, err
	}
	return advanceRuntimeMutation(ctx, r, operation, true)
}

// ObserveRuntime never publishes configuration or requests a service restart.
// It may durably confirm an already completed pending mutation, including after
// the operation deadline. Unknown native observations never become ready.
func ObserveRuntime(ctx context.Context, operation RuntimeOperation, finalSystem string) (RuntimeReceipt, error) {
	r, err := openRuntime(operation, finalSystem, false)
	if errors.Is(err, os.ErrNotExist) && len(operation.Snapshot.Bindings) == 0 {
		return receiptFor(operation, runtimeLedger{}, RuntimeNeutral), nil
	}
	if err != nil {
		return RuntimeReceipt{}, err
	}
	defer r.close()
	if r.ledger.SchemaVersion == 0 {
		return receiptFor(operation, r.ledger, RuntimeNeutral), nil
	}
	return observeRuntime(ctx, r, operation)
}

func observeRuntime(ctx context.Context, r *runtimeRoots, operation RuntimeOperation) (RuntimeReceipt, error) {
	if r.ledger.SchemaVersion == 0 {
		return receiptFor(operation, r.ledger, RuntimeNeutral), nil
	}
	if r.ledger.Pending != nil {
		if r.ledger.Pending.Key != operationKey(operation) {
			return RuntimeReceipt{}, ErrRuntimeBusy
		}
		return advanceRuntimeMutation(ctx, r, operation, false)
	}
	index := environmentIndex(&r.ledger, operation.Snapshot.EnvironmentID)
	if index < 0 {
		return RuntimeReceipt{}, ErrRuntimeState
	}
	environment := r.ledger.Environments[index]
	phase := RuntimeReady
	if environment.Restored != nil && *environment.Restored == operationKey(operation) {
		phase = RuntimeRestored
	} else if environment.Current == nil || environment.Current.Key != operationKey(operation) {
		return RuntimeReceipt{}, ErrRuntimeConflict
	}
	configuration, err := runtimeConfiguration(r.ledger, nil)
	if err != nil {
		return RuntimeReceipt{}, err
	}
	observed, err := ObserveTunnelRuntime(ctx, r.finalSystem, r.connector, r.credentials, configuration)
	if err != nil {
		return RuntimeReceipt{}, err
	}
	if !observed.Ready {
		return receiptFor(operation, r.ledger, RuntimeApplying), nil
	}
	return receiptFor(operation, r.ledger, phase), nil
}

func advanceRuntimeMutation(ctx context.Context, r *runtimeRoots, operation RuntimeOperation, effects bool) (RuntimeReceipt, error) {
	pending := r.ledger.Pending
	if pending == nil || pending.Key != operationKey(operation) {
		return RuntimeReceipt{}, ErrRuntimeState
	}
	if r.ledger.Revision == math.MaxUint64 {
		return RuntimeReceipt{}, ErrCapacity
	}
	configuration, err := runtimeConfiguration(r.ledger, pending)
	if err != nil || configurationHash(configuration) != pending.ConfigSHA256 {
		return RuntimeReceipt{}, ErrRuntimeState
	}
	disk, err := readRuntimeFile(r.connector, "config.json", 0644, MaxConfigBytes)
	if err != nil {
		return RuntimeReceipt{}, err
	}
	digest := configurationHash(disk)
	if digest != pending.FromConfigSHA256 && digest != pending.ConfigSHA256 {
		return RuntimeReceipt{}, ErrRuntimeState
	}
	if digest != pending.ConfigSHA256 {
		if !effects {
			return receiptFor(operation, r.ledger, RuntimeApplying), nil
		}
		if err := writeRuntimeFile(ctx, r.connector, "config.json", configuration, 0644, MaxConfigBytes); err != nil {
			return RuntimeReceipt{}, err
		}
	}
	if pending.ConfigSHA256 != pending.FromConfigSHA256 && !pending.RestartRequested {
		if !effects {
			return receiptFor(operation, r.ledger, RuntimeApplying), nil
		}
		// Capture the process after publication. A different process observed
		// only before publication could still have loaded the previous config.
		before, err := ObserveTunnelRuntime(ctx, r.finalSystem, r.connector, r.credentials, configuration)
		if err != nil {
			return RuntimeReceipt{}, err
		}
		pending.BeforeProcess, pending.RestartRequested = before.Process, true
		if err := saveRuntimeLedger(ctx, r.state, r.ledger); err != nil {
			return RuntimeReceipt{}, err
		}
		// The request flag is one-way for this pending intent. An interrupted
		// native restart is reconciled from a changed process, never reissued.
		_ = restartTunnel(ctx, r.finalSystem)
		return receiptFor(operation, r.ledger, RuntimeApplying), nil
	}
	observed, err := ObserveTunnelRuntime(ctx, r.finalSystem, r.connector, r.credentials, configuration)
	if err != nil {
		return RuntimeReceipt{}, err
	}
	if !runtimeMutationReady(*pending, observed) {
		return receiptFor(operation, r.ledger, RuntimeApplying), nil
	}
	return confirmRuntimeMutation(ctx, r, operation)
}

func runtimeMutationReady(pending runtimePending, observed TunnelObservation) bool {
	return observed.Ready && (pending.ConfigSHA256 == pending.FromConfigSHA256 || observed.Process != "" && observed.Process != pending.BeforeProcess)
}

// This records only the route mutation whose native observation was verified
// by advanceRuntimeMutation. It does not authorize any external effect.
func confirmRuntimeMutation(ctx context.Context, r *runtimeRoots, operation RuntimeOperation) (RuntimeReceipt, error) {
	pending := r.ledger.Pending
	if pending == nil || pending.Key != operationKey(operation) {
		return RuntimeReceipt{}, ErrRuntimeState
	}
	if r.ledger.Revision == math.MaxUint64 {
		return RuntimeReceipt{}, ErrCapacity
	}
	index := environmentIndex(&r.ledger, pending.Key.EnvironmentID)
	if index < 0 {
		r.ledger.Environments = append(r.ledger.Environments, runtimeEnvironment{EnvironmentID: pending.Key.EnvironmentID})
		index = len(r.ledger.Environments) - 1
	}
	environment := &r.ledger.Environments[index]
	phase := RuntimeReady
	if pending.Restore {
		allocation, err := findRuntimeAllocation(r.ledger, pending.Key)
		if err != nil {
			return RuntimeReceipt{}, err
		}
		environment.Current, environment.Previous = pending.Target, allocation.PriorPrevious
		key := pending.Key
		environment.Restored = &key
		phase = RuntimeRestored
	} else {
		environment.Previous, environment.Current = environment.Current, pending.Target
		environment.Restored = nil
	}
	r.ledger.Revision++
	r.ledger.ConfigSHA256 = pending.ConfigSHA256
	r.ledger.Pending = nil
	if err := saveRuntimeLedger(ctx, r.state, r.ledger); err != nil {
		return RuntimeReceipt{}, err
	}
	return receiptFor(operation, r.ledger, phase), nil
}

// RestoreRuntime is called after the runner restores and verifies the exact
// previous Compose runtime. It never restores a historical whole-server config.
func RestoreRuntime(ctx context.Context, operation RuntimeOperation, finalSystem string) (RuntimeReceipt, error) {
	ctx, cancel, err := runtimeContext(ctx, operation)
	if err != nil {
		return RuntimeReceipt{}, err
	}
	defer cancel()
	r, err := openRuntime(operation, finalSystem, false)
	if errors.Is(err, os.ErrNotExist) && len(operation.Snapshot.Bindings) == 0 {
		return receiptFor(operation, runtimeLedger{}, RuntimeNeutral), nil
	}
	if err != nil {
		return RuntimeReceipt{}, err
	}
	defer r.close()
	if r.ledger.SchemaVersion == 0 {
		return receiptFor(operation, r.ledger, RuntimeNeutral), nil
	}
	key := operationKey(operation)
	if r.ledger.Pending != nil && (r.ledger.Pending.Key != key) {
		return RuntimeReceipt{}, ErrRuntimeBusy
	}
	if r.ledger.Pending != nil && r.ledger.Pending.Restore {
		return advanceRuntimeMutation(ctx, r, operation, true)
	}
	allocation, err := findRuntimeAllocation(r.ledger, key)
	if err != nil {
		return RuntimeReceipt{}, err
	}
	index := environmentIndex(&r.ledger, key.EnvironmentID)
	var target *runtimeContribution
	if index >= 0 {
		environment := r.ledger.Environments[index]
		if environment.Restored != nil && *environment.Restored == key && r.ledger.Pending == nil {
			return observeRuntime(ctx, r, operation)
		}
		target = environment.Current
		if target != nil && target.Key != key {
			if allocation.PriorCurrent == nil || target.Key != allocation.PriorCurrent.Key {
				return RuntimeReceipt{}, ErrRuntimeConflict
			}
		} else if target == nil && allocation.PriorCurrent != nil {
			return RuntimeReceipt{}, ErrRuntimeConflict
		}
		if target != nil && target.Key == key {
			target = environment.Previous
		}
		if target != nil && target.Key.ReleaseID != operation.ExpectedCurrentReleaseID {
			return RuntimeReceipt{}, ErrRuntimeConflict
		}
	}
	disk, err := readRuntimeFile(r.connector, "config.json", 0644, MaxConfigBytes)
	if err != nil {
		return RuntimeReceipt{}, err
	}
	digest := configurationHash(disk)
	if r.ledger.Pending == nil {
		if digest != r.ledger.ConfigSHA256 {
			return RuntimeReceipt{}, ErrRuntimeState
		}
	} else if digest != r.ledger.Pending.ConfigSHA256 && digest != r.ledger.Pending.FromConfigSHA256 {
		return RuntimeReceipt{}, ErrRuntimeState
	}
	pending := runtimePending{Key: key, Restore: true, FromRevision: r.ledger.Revision, FromConfigSHA256: digest, Target: target}
	if r.ledger.Revision == math.MaxUint64 {
		return RuntimeReceipt{}, ErrCapacity
	}
	configuration, err := runtimeConfiguration(r.ledger, &pending)
	if err != nil {
		return RuntimeReceipt{}, err
	}
	pending.ConfigSHA256 = configurationHash(configuration)
	if err := ValidateTunnelConfiguration(ctx, r.connector, configuration, nativeTunnelTools(finalSystem).cloudflared); err != nil {
		return RuntimeReceipt{}, err
	}
	r.ledger.Pending = &pending
	if err := saveRuntimeLedger(ctx, r.state, r.ledger); err != nil {
		return RuntimeReceipt{}, err
	}
	return advanceRuntimeMutation(ctx, r, operation, true)
}

// CancelRuntimePreparation releases only a never-applied allocation. The runner
// must hold protected preparing-phase authority proving no Compose replacement
// and no Apply attempt. Cancellation needs no app health and may settle local
// allocation state after the execution deadline; it performs no native effects.
func CancelRuntimePreparation(ctx context.Context, operation RuntimeOperation, finalSystem string) error {
	r, err := openRuntime(operation, finalSystem, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer r.close()
	return cancelRuntimePreparation(ctx, r, operation)
}

func cancelRuntimePreparation(ctx context.Context, r *runtimeRoots, operation RuntimeOperation) error {
	key := operationKey(operation)
	if r.ledger.Pending != nil {
		return ErrRuntimeBusy
	}
	if allocationReferenced(r.ledger, key) {
		return ErrRuntimeConflict
	}
	index := environmentIndex(&r.ledger, key.EnvironmentID)
	if index >= 0 && r.ledger.Environments[index].Restored != nil && *r.ledger.Environments[index].Restored == key {
		return ErrRuntimeConflict
	}
	for i, allocation := range r.ledger.Allocations {
		if allocation.Key.OperationID != key.OperationID {
			continue
		}
		if allocation.Key != key {
			return ErrRuntimeConflict
		}
		if err := checkRuntimeBaseline(r.ledger, allocation); err != nil {
			return err
		}
		r.ledger.Allocations = slices.Delete(r.ledger.Allocations, i, i+1)
		return saveRuntimeLedger(ctx, r.state, r.ledger)
	}
	return nil
}

// OwnedConfiguration is the provisioning observer's read-only view. An absent
// ledger means only the original rejecting configuration. Corruption or an
// unresolved shared config mutation must never fall back to that initial file.
func OwnedConfiguration(installationID, serverID, accountID, tunnelID string) ([]byte, error) {
	root, err := openRuntimeRoot(runtimeStateDirectory, 0700)
	if errors.Is(err, os.ErrNotExist) {
		return Compile(tunnelID, nil, nil)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	lock, err := runtimeLock(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Close() }()
	if runtimeDirectoryEntries(root, "routes.lock", "ledger.json", "ledger.json.next") != nil {
		return nil, ErrRuntimeState
	}
	var ledger runtimeLedger
	if err := readRuntimeRecord(root, "ledger.json", &ledger); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Compile(tunnelID, nil, nil)
		}
		return nil, ErrRuntimeState
	}
	return ownedRuntimeConfiguration(ledger, installationID, serverID, accountID, tunnelID)
}

func ownedRuntimeConfiguration(ledger runtimeLedger, installationID, serverID, accountID, tunnelID string) ([]byte, error) {
	if validateRuntimeLedger(ledger) != nil || ledger.InstallationID != installationID || ledger.ServerID != serverID || ledger.AccountID != accountID || ledger.TunnelID != tunnelID {
		return nil, ErrRuntimeState
	}
	if ledger.Pending != nil {
		return nil, ErrTunnelRuntime
	}
	return runtimeConfiguration(ledger, nil)
}

func checkRuntimeBaseline(ledger runtimeLedger, allocation runtimeAllocation) error {
	index := environmentIndex(&ledger, allocation.Key.EnvironmentID)
	var current *runtimeContribution
	if index >= 0 {
		current = ledger.Environments[index].Current
	}
	if current == nil && allocation.PriorCurrent == nil {
		return nil
	}
	if current != nil && allocation.PriorCurrent != nil && current.Key == allocation.PriorCurrent.Key {
		return nil
	}
	return ErrRuntimeConflict
}

func planRuntimeApply(ledger runtimeLedger, operation RuntimeOperation, expectedRevision uint64, expectedConfigSHA256 string) (runtimePending, []byte, error) {
	key := operationKey(operation)
	if ledger.Revision != expectedRevision || ledger.ConfigSHA256 != expectedConfigSHA256 {
		return runtimePending{}, nil, ErrRuntimeConflict
	}
	if err := runtimeExpectedCurrent(&ledger, operation); err != nil {
		return runtimePending{}, nil, err
	}
	allocation, err := findRuntimeAllocation(ledger, key)
	if err != nil {
		return runtimePending{}, nil, err
	}
	if err := checkRuntimeBaseline(ledger, allocation); err != nil {
		return runtimePending{}, nil, err
	}
	if ledger.Revision == math.MaxUint64 {
		return runtimePending{}, nil, ErrCapacity
	}
	target := contributionFor(operation, allocation.Origins)
	pending := runtimePending{Key: key, FromRevision: ledger.Revision, FromConfigSHA256: ledger.ConfigSHA256, Target: &target}
	configuration, err := runtimeConfiguration(ledger, &pending)
	if err != nil {
		return runtimePending{}, nil, err
	}
	pending.ConfigSHA256 = configurationHash(configuration)
	return pending, configuration, nil
}
