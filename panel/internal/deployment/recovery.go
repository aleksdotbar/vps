package deployment

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/routes"
)

func recoverRelease(ctx context.Context, environment, registryRoot *os.Root, value *execution, tools runnerTools, acquire EffectAuthority) error {
	if terminalPhase(value.Phase) {
		return nil
	}
	_ = cleanupRegistry(registryRoot, value.Claim.Identity.OperationID)
	main, err := observeRunnerUnit(ctx, tools, *value, false)
	if err != nil || main.running() || !main.absent() && (!runnerUnitOwned(main, *value, false) || value.InvocationID != "" && main.InvocationID != value.InvocationID) {
		return ErrState
	}
	boot, err := nativeBootID()
	if err != nil || boot != value.BootID {
		return writePhase(ctx, environment, value, "needs_attention", "native_execution_unknown")
	}
	if value.Phase == "starting" || value.Phase == "preparing" {
		return cancelPreparation(ctx, environment, registryRoot, value, tools, acquire, "preparation_interrupted")
	}
	candidate, err := material(environment, releaseReference{value.Claim.Identity, value.Claim.ManifestSHA256})
	defer clear(candidate.RuntimeConfig)
	defer clear(candidate.Stage.ApplicationConfig)
	if err != nil {
		return writePhase(ctx, environment, value, "needs_attention", "retained_material_unavailable")
	}
	var previous *releaseMaterial
	if value.Previous != nil {
		retained, err := material(environment, *value.Previous)
		if err != nil {
			return writePhase(ctx, environment, value, "needs_attention", "retained_material_unavailable")
		}
		previous = &retained
		defer clear(retained.RuntimeConfig)
		defer clear(retained.Stage.ApplicationConfig)
	}
	if verifyProjectOwnership(ctx, tools, candidate, previous) != nil {
		return writePhase(ctx, environment, value, "needs_attention", "runtime_ownership_unknown")
	}
	if value.RecoveryStarted {
		// An interrupted recovery permits observation/finalization only, never a
		// second Compose replacement or route mutation whose outcome is uncertain.
		if previous == nil {
			return writePhase(ctx, environment, value, "needs_attention", "recovery_interrupted")
		}
		if _, err := observeContainers(ctx, tools, *previous, true); err != nil || awaitOrigins(ctx, *previous) != nil {
			return writePhase(ctx, environment, value, "needs_attention", "recovery_interrupted")
		}
		receipt, err := routes.ObserveRuntime(ctx, operation(candidate.Stage), tools.system)
		if err != nil || !routeReceiptMatches(receipt, candidate.Stage, previous, true) {
			return writePhase(ctx, environment, value, "needs_attention", "recovery_interrupted")
		}
		return finishRecovery(ctx, environment, value, receipt, acquire)
	}
	next := *value
	next.Phase = "recovering"
	next.Reason = "candidate_failed"
	next.RecoveryStarted = true
	if replaceExecution(ctx, environment, *value, next) != nil {
		return ErrState
	}
	*value = next
	if previous == nil {
		err := withAuthority(ctx, acquire, func() error { return composeCommand(ctx, tools, candidate.RuntimeConfig, "down", "--remove-orphans") })
		if err != nil {
			return writePhase(ctx, environment, value, "needs_attention", "first_release_cleanup_failed")
		}
		ids, err := containerIDs(ctx, tools, value.Claim.Identity)
		if err != nil || len(ids) != 0 {
			return writePhase(ctx, environment, value, "needs_attention", "first_release_cleanup_failed")
		}
	} else {
		for service, artifact := range previous.Stage.Manifest.Artifact.Services {
			id, err := inspectImage(ctx, tools, artifact.Image)
			if err != nil || id != previous.ImageIDs[service] {
				return writePhase(ctx, environment, value, "needs_attention", "retained_image_unavailable")
			}
		}
		err := withAuthority(ctx, acquire, func() error {
			return composeCommand(ctx, tools, previous.RuntimeConfig, "up", "--detach", "--no-build", "--pull", "never", "--remove-orphans")
		})
		if err != nil || awaitHealth(ctx, tools, *previous) != nil {
			return writePhase(ctx, environment, value, "needs_attention", "recovery_health_failed")
		}
	}
	var receipt routes.RuntimeReceipt
	err = withAuthority(ctx, acquire, func() error {
		var err error
		receipt, err = routes.RestoreRuntime(ctx, operation(candidate.Stage), tools.system)
		return err
	})
	if err != nil {
		return writePhase(ctx, environment, value, "needs_attention", "recovery_routes_unknown")
	}
	for receipt.Phase == routes.RuntimeApplying {
		if waitObservation(ctx) != nil {
			return ErrDeadline
		}
		receipt, err = routes.ObserveRuntime(ctx, operation(candidate.Stage), tools.system)
		if err != nil {
			return writePhase(ctx, environment, value, "needs_attention", "recovery_routes_unknown")
		}
	}
	if !routeReceiptMatches(receipt, candidate.Stage, previous, true) {
		return writePhase(ctx, environment, value, "needs_attention", "recovery_routes_unknown")
	}
	return finishRecovery(ctx, environment, value, receipt, acquire)
}

func finishRecovery(ctx context.Context, environment *os.Root, value *execution, receipt routes.RuntimeReceipt, acquire EffectAuthority) error {
	return withAuthority(ctx, acquire, func() error {
		current, err := readCurrent(environment)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		candidate := releaseReference{value.Claim.Identity, value.Claim.ManifestSHA256}
		if !sameReference(current, value.Previous) {
			if current == nil || *current != candidate {
				return ErrState
			}
			if value.Previous == nil {
				if environment.Remove("current.json") != nil || syncDirectory(environment) != nil {
					return ErrState
				}
			} else if replaceRecord(ctx, environment, "current.json", current, value.Previous) != nil {
				return ErrState
			}
		}
		next := *value
		next.RouteReceipt = receipt
		if replaceExecution(ctx, environment, *value, next) != nil {
			return ErrState
		}
		*value = next
		phase, reason := "recovered", ""
		if value.Previous == nil {
			phase, reason = "failed", "first_release_failed"
		}
		return writePhase(ctx, environment, value, phase, reason)
	})
}

func Observe(ctx context.Context, root *os.Root, request ExecutionRequest, finalSystem string) (ExecutionResponse, error) {
	environment, expected, err := executionEnvironment(root, request, finalSystem)
	if err != nil {
		return ExecutionResponse{}, err
	}
	defer func() { _ = environment.Close() }()
	value, err := readExecution(environment, expected)
	if err != nil {
		return ExecutionResponse{}, err
	}
	var previous *releaseMaterial
	if value.Previous != nil {
		retained, err := material(environment, *value.Previous)
		if err != nil {
			return ExecutionResponse{}, err
		}
		previous = &retained
		defer clear(retained.RuntimeConfig)
		defer clear(retained.Stage.ApplicationConfig)
	}
	result := executionResponse(request, value)
	tools := systemTools(finalSystem)
	if value.Phase == "awaiting_dns" {
		unit, err := observeRunnerUnit(ctx, tools, value, false)
		if err != nil || !runnerUnitOwned(unit, value, false) || !unit.running() || verifyRunnerProcess(unit, tools, value, false) != nil {
			return ExecutionResponse{}, ErrState
		}
	}
	if value.Phase == "awaiting_dns" || value.Phase == "succeeded" || value.Phase == "recovered" {
		reference := releaseReference{expected.Identity, expected.ManifestSHA256}
		if value.Phase == "recovered" {
			if value.Previous == nil {
				return ExecutionResponse{}, ErrState
			}
			reference = *value.Previous
		}
		retained, err := material(environment, reference)
		defer clear(retained.RuntimeConfig)
		defer clear(retained.Stage.ApplicationConfig)
		if err != nil {
			return ExecutionResponse{}, err
		}
		result.Services, err = observeContainers(ctx, tools, retained, true)
		if err != nil || awaitOrigins(ctx, retained) != nil {
			return ExecutionResponse{}, ErrState
		}
		stage, err := readStage(environment, expected)
		defer clear(stage.ApplicationConfig)
		if err != nil {
			return ExecutionResponse{}, err
		}
		receipt, err := routes.ObserveRuntime(ctx, operation(stage), finalSystem)
		if err != nil {
			return ExecutionResponse{}, err
		}
		if !routeReceiptMatches(receipt, stage, previous, value.Phase == "recovered") {
			return ExecutionResponse{}, ErrState
		}
		result.RouteRevision, result.RouteConfigSHA256 = receipt.Revision, receipt.ConfigSHA256
		if value.Phase != "awaiting_dns" {
			current, err := readCurrent(environment)
			if err != nil || current == nil || *current != reference {
				return ExecutionResponse{}, ErrState
			}
		}
		return result, nil
	}
	if !terminalPhase(value.Phase) {
		if time.Now().UnixMilli() >= expected.Identity.ExecutionDeadline {
			result.Phase, result.Reason = "needs_attention", "execution_deadline_exceeded"
			return result, nil
		}
		recovery := value.Phase == "recovering"
		unit, err := observeRunnerUnit(ctx, tools, value, recovery)
		if err != nil || !runnerUnitOwned(unit, value, recovery) || !unit.running() || verifyRunnerProcess(unit, tools, value, recovery) != nil {
			return ExecutionResponse{}, ErrState
		}
	}
	return result, nil
}

func Confirm(ctx context.Context, root *os.Root, request ExecutionRequest, finalSystem string, acquire EffectAuthority) (ExecutionResponse, error) {
	if time.Now().UnixMilli() >= request.Identity.ExecutionDeadline {
		return ExecutionResponse{}, ErrDeadline
	}
	result, err := Observe(ctx, root, request, finalSystem)
	if err != nil {
		return ExecutionResponse{}, err
	}
	if result.Phase == "succeeded" {
		return result, nil
	}
	if result.Phase != "awaiting_dns" {
		return ExecutionResponse{}, ErrState
	}
	environment, expected, err := executionEnvironment(root, request, finalSystem)
	if err != nil {
		return ExecutionResponse{}, err
	}
	defer func() { _ = environment.Close() }()
	err = withAuthority(ctx, acquire, func() error { return saveConfirmation(ctx, environment, expected) })
	return result, err
}

func cancelPreparation(ctx context.Context, environment, registryRoot *os.Root, value *execution, tools runnerTools, acquire EffectAuthority, reason string) error {
	if value.Phase != "starting" && value.Phase != "preparing" {
		return ErrState
	}
	if value.Previous == nil {
		ids, err := containerIDs(ctx, tools, value.Claim.Identity)
		if err != nil || len(ids) != 0 {
			return writePhase(ctx, environment, value, "needs_attention", "runtime_ownership_unknown")
		}
	} else {
		previous, err := material(environment, *value.Previous)
		defer clear(previous.RuntimeConfig)
		defer clear(previous.Stage.ApplicationConfig)
		if err != nil {
			return writePhase(ctx, environment, value, "needs_attention", "retained_material_unavailable")
		}
		if err := verifyProjectOwnership(ctx, tools, previous, nil); err != nil {
			return writePhase(ctx, environment, value, "needs_attention", "runtime_ownership_unknown")
		}
	}
	stage, err := readStage(environment, value.Claim)
	defer clear(stage.ApplicationConfig)
	if err != nil {
		return err
	}
	for {
		err = withAuthority(ctx, acquire, func() error { return routes.CancelRuntimePreparation(ctx, operation(stage), tools.system) })
		if !errors.Is(err, routes.ErrRuntimeBusy) {
			break
		}
		if waitObservation(ctx) != nil {
			return ErrDeadline
		}
	}
	if err != nil {
		return writePhase(ctx, environment, value, "needs_attention", "recovery_routes_unknown")
	}
	return writePhase(ctx, environment, value, "failed", reason)
}
