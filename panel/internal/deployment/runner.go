package deployment

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/routes"
	"github.com/google/uuid"
)

// EffectAuthority acquires the already-settled native host/readiness lease.
// The returned release function is held only across a concrete short mutation,
// never image pulls or application-health polling.
type EffectAuthority func(context.Context) (func() error, error)

func withAuthority(ctx context.Context, acquire EffectAuthority, effect func() error) error {
	if acquire == nil {
		return ErrState
	}
	release, err := acquire(ctx)
	if err != nil || release == nil {
		return ErrState
	}
	effectErr := effect()
	if release() != nil {
		return ErrState
	}
	return effectErr
}

func operation(value staged) routes.RuntimeOperation {
	snapshot := value.Manifest.RouteSnapshot
	snapshot.SHA256 = value.Manifest.RouteSnapshotSHA256
	return routes.RuntimeOperation{InstallationID: value.Claim.Identity.InstallationID, Snapshot: snapshot, ManifestSHA256: value.Claim.ManifestSHA256, ExpectedCurrentReleaseID: value.Claim.Identity.ExpectedCurrentReleaseID, ExecutionDeadline: value.Claim.Identity.ExecutionDeadline}
}

func executionEnvironment(root *os.Root, request ExecutionRequest, finalSystem string) (*os.Root, claim, error) {
	info, err := root.Stat(".")
	if err != nil || !protected(info, true) || request.SchemaVersion != 1 || !executionAction(request.Action) || !validHash(request.Challenge) || ValidateIdentity(request.Identity) != nil || !validHash(request.ManifestSHA256) || !finalSystemPath.MatchString(finalSystem) {
		return nil, claim{}, ErrInvalid
	}
	expected := claim{request.Identity, request.ManifestSHA256, finalSystem}
	environment, err := child(root, request.Identity.EnvironmentID, false)
	if err != nil {
		return nil, claim{}, err
	}
	var active claim
	if readRecord(environment, "active.json", &active) != nil || active != expected {
		_ = environment.Close()
		return nil, claim{}, ErrState
	}
	return environment, expected, nil
}

func Start(ctx context.Context, root *os.Root, request ExecutionRequest, finalSystem string, acquire EffectAuthority) (ExecutionResponse, error) {
	environment, expected, err := executionEnvironment(root, request, finalSystem)
	if err != nil {
		return ExecutionResponse{}, err
	}
	defer func() { _ = environment.Close() }()
	lock, err := executionLock(environment)
	if errors.Is(err, ErrBusy) {
		return Observe(ctx, root, request, finalSystem)
	}
	if err != nil {
		return ExecutionResponse{}, err
	}
	defer func() { _ = lock.Close() }()
	if _, err := readExecution(environment, expected); err == nil {
		return Observe(ctx, root, request, finalSystem)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ExecutionResponse{}, ErrState
	}
	if time.Until(time.UnixMilli(request.Identity.ExecutionDeadline)) <= recoveryReserve+time.Minute {
		return ExecutionResponse{}, ErrDeadline
	}
	stage, err := readStage(environment, expected)
	defer clear(stage.ApplicationConfig)
	if err != nil {
		return ExecutionResponse{}, err
	}
	current, err := readCurrent(environment)
	if err != nil && !errors.Is(err, os.ErrNotExist) || current == nil && expected.Identity.ExpectedCurrentReleaseID != "" || current != nil && !expectedCurrent(*current, expected.Identity) {
		return ExecutionResponse{}, ErrState
	}
	if current != nil {
		retained, err := material(environment, *current)
		clear(retained.RuntimeConfig)
		clear(retained.Stage.ApplicationConfig)
		if err != nil {
			return ExecutionResponse{}, err
		}
	}
	boot, err := nativeBootID()
	if err != nil {
		return ExecutionResponse{}, err
	}
	value := execution{Claim: expected, LaunchID: uuid.NewString(), Phase: "starting", Previous: current, BootID: boot}
	tools := systemTools(finalSystem)
	for _, recovery := range []bool{false, true} {
		unit, err := observeRunnerUnit(ctx, tools, value, recovery)
		if err != nil || !unit.absent() {
			return ExecutionResponse{}, ErrState
		}
	}
	if err := withAuthority(ctx, acquire, func() error {
		if putExecution(ctx, environment, value) != nil {
			return ErrState
		}
		// Arm recovery before admitting the main unit. A lost launch response leaves
		// a durable unknown outcome; Start never relaunches an existing intent.
		if launchRunner(ctx, tools, value, true) != nil {
			return ErrState
		}
		return launchRunner(ctx, tools, value, false)
	}); err != nil {
		return ExecutionResponse{}, err
	}
	return Observe(ctx, root, request, finalSystem)
}

func waitExecutionLock(ctx context.Context, environment *os.Root) (*os.File, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		lock, err := executionLock(environment)
		if !errors.Is(err, ErrBusy) {
			return lock, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Run is invoked only by the fixed native transient service. It accepts no
// command, configuration, secret or target pathname from its process arguments.
func Run(ctx context.Context, root *os.Root, environmentID, operationID, finalSystem string, recovery bool, acquire EffectAuthority) error {
	if !validID(environmentID) || !validID(operationID) || !finalSystemPath.MatchString(finalSystem) {
		return ErrInvalid
	}
	environment, err := child(root, environmentID, false)
	if err != nil {
		return err
	}
	defer func() { _ = environment.Close() }()
	var active claim
	if readRecord(environment, "active.json", &active) != nil || active.Identity.OperationID != operationID || active.FinalSystem != finalSystem {
		return ErrState
	}
	ctx, cancel := context.WithDeadline(ctx, time.UnixMilli(active.Identity.ExecutionDeadline))
	defer cancel()
	lockCtx, lockCancel := context.WithTimeout(ctx, 30*time.Second)
	lock, err := waitExecutionLock(lockCtx, environment)
	lockCancel()
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	value, err := readExecution(environment, active)
	if err != nil {
		return err
	}
	registryRoot, _ := openRegistryRoot(false)
	if registryRoot != nil {
		defer func() { _ = registryRoot.Close() }()
	}
	tools := systemTools(finalSystem)
	defer func() {
		retained, err := readExecution(environment, active)
		if err == nil && terminalPhase(retained.Phase) {
			_ = stopRecoveryTimer(context.WithoutCancel(ctx), tools, retained)
		}
	}()
	if err := verifyRunnerSelf(ctx, tools, value, recovery); err != nil {
		return err
	}
	if recovery {
		return recoverRelease(ctx, environment, registryRoot, &value, tools, acquire)
	}
	if value.Phase != "starting" {
		return ErrState
	}
	unit, err := observeRunnerUnit(ctx, tools, value, false)
	if err != nil {
		return err
	}
	next := value
	next.InvocationID = unit.InvocationID
	if replaceExecution(ctx, environment, value, next) != nil {
		return ErrState
	}
	value = next
	forward, cancelForward := context.WithDeadline(ctx, time.UnixMilli(active.Identity.ExecutionDeadline).Add(-recoveryReserve-30*time.Second))
	defer cancelForward()
	return executeRelease(forward, ctx, environment, registryRoot, &value, tools, acquire)
}

func executeRelease(ctx, recoveryContext context.Context, environment, registryRoot *os.Root, value *execution, tools runnerTools, acquire EffectAuthority) error {
	stage, err := readStage(environment, value.Claim)
	defer clear(stage.ApplicationConfig)
	if err != nil {
		return err
	}
	if writePhase(ctx, environment, value, "preparing", "") != nil {
		return ErrState
	}
	failBeforeReplacement := func(step string) error {
		slog.Error("deployment preparation failed", "operation", value.Claim.Identity.OperationID, "step", step)
		_ = cleanupRegistry(registryRoot, value.Claim.Identity.OperationID)
		return cancelPreparation(recoveryContext, environment, registryRoot, value, tools, acquire, "preparation_failed")
	}

	if withAuthority(ctx, acquire, func() error { return nil }) != nil {
		return failBeforeReplacement("host readiness")
	}
	images, err := prepareImages(ctx, registryRoot, stage, tools)
	if err != nil {
		return failBeforeReplacement("image preparation")
	}
	var prepared routes.RuntimePreparation
	err = withAuthority(ctx, acquire, func() error {
		var err error
		prepared, err = routes.PrepareRuntime(ctx, operation(stage), value.Claim.FinalSystem)
		return err
	})
	if err != nil {
		return failBeforeReplacement("route preparation")
	}
	next := *value
	next.Preparation = prepared
	if replaceExecution(ctx, environment, *value, next) != nil {
		return ErrState
	}
	*value = next
	config, err := runtimeCompose(stage, prepared.Origins)
	defer clear(config)
	if err != nil {
		return failBeforeReplacement("Compose configuration")
	}
	candidate := releaseMaterial{stage, config, prepared.Origins, images}
	if validateNative(ctx, config, validationTool{tools.docker, []string{"compose"}}) != nil {
		return failBeforeReplacement("Compose validation")
	}
	var previous *releaseMaterial
	if value.Previous != nil {
		retained, err := material(environment, *value.Previous)
		if err != nil {
			return failBeforeReplacement("previous release")
		}
		previous = &retained
		defer clear(retained.RuntimeConfig)
		defer clear(retained.Stage.ApplicationConfig)
	}
	if verifyProjectOwnership(ctx, tools, candidate, previous) != nil {
		return failBeforeReplacement("project ownership")
	}
	if saveMaterial(ctx, environment, candidate) != nil {
		return failBeforeReplacement("release material")
	}
	if writePhase(ctx, environment, value, "replacing", "") != nil {
		return ErrState
	}
	replaceCtx, replaceCancel := context.WithTimeout(ctx, 2*time.Minute)
	err = withAuthority(replaceCtx, acquire, func() error {
		return composeCommand(replaceCtx, tools, config, "up", "--detach", "--no-build", "--pull", "never", "--remove-orphans")
	})
	replaceCancel()
	if err != nil {
		return scheduleRecovery(recoveryContext, environment, value, tools, candidate, previous)
	}
	if writePhase(ctx, environment, value, "checking", "") != nil {
		return ErrState
	}
	if awaitHealth(ctx, tools, candidate) != nil {
		return scheduleRecovery(recoveryContext, environment, value, tools, candidate, previous)
	}
	if writePhase(ctx, environment, value, "applying_routes", "") != nil {
		return ErrState
	}
	var receipt routes.RuntimeReceipt
	for {
		err = withAuthority(ctx, acquire, func() error {
			var err error
			receipt, err = routes.ApplyRuntime(ctx, operation(stage), prepared.Revision, prepared.ConfigSHA256, value.Claim.FinalSystem)
			return err
		})
		if !errors.Is(err, routes.ErrRuntimeConflict) && !errors.Is(err, routes.ErrRuntimeBusy) {
			break
		}
		if waitObservation(ctx) != nil {
			return scheduleRecovery(recoveryContext, environment, value, tools, candidate, previous)
		}
		err = withAuthority(ctx, acquire, func() error {
			var err error
			prepared, err = routes.PrepareRuntime(ctx, operation(stage), value.Claim.FinalSystem)
			return err
		})
		if errors.Is(err, routes.ErrRuntimeBusy) {
			continue
		}
		if err != nil || !slices.Equal(prepared.Origins, candidate.Origins) {
			return scheduleRecovery(recoveryContext, environment, value, tools, candidate, previous)
		}
		if _, err := observeContainers(ctx, tools, candidate, true); err != nil {
			return scheduleRecovery(recoveryContext, environment, value, tools, candidate, previous)
		}
	}
	if err != nil {
		return scheduleRecovery(recoveryContext, environment, value, tools, candidate, previous)
	}
	for receipt.Phase == routes.RuntimeApplying {
		if waitObservation(ctx) != nil {
			return scheduleRecovery(recoveryContext, environment, value, tools, candidate, previous)
		}
		receipt, err = routes.ObserveRuntime(ctx, operation(stage), value.Claim.FinalSystem)
		if err != nil {
			return scheduleRecovery(recoveryContext, environment, value, tools, candidate, previous)
		}
	}
	if !routeReceiptMatches(receipt, stage, previous, false) {
		return scheduleRecovery(recoveryContext, environment, value, tools, candidate, previous)
	}
	next = *value
	next.RouteReceipt = receipt
	if replaceExecution(ctx, environment, *value, next) != nil {
		return ErrState
	}
	*value = next
	if writePhase(ctx, environment, value, "awaiting_dns", "") != nil {
		return ErrState
	}
	for {
		confirmed, err := readConfirmation(environment, value.Claim)
		if err != nil {
			return err
		}
		if confirmed {
			if _, err := observeContainers(ctx, tools, candidate, true); err != nil || awaitOrigins(ctx, candidate) != nil {
				return scheduleRecovery(recoveryContext, environment, value, tools, candidate, previous)
			}
			route, err := routes.ObserveRuntime(ctx, operation(stage), value.Claim.FinalSystem)
			if err != nil || !routeReceiptMatches(route, stage, previous, false) {
				return scheduleRecovery(recoveryContext, environment, value, tools, candidate, previous)
			}
			return withAuthority(ctx, acquire, func() error {
				if commitCurrent(ctx, environment, *value) != nil {
					return ErrState
				}
				return writePhase(ctx, environment, value, "succeeded", "")
			})
		}
		if waitObservation(ctx) != nil {
			return scheduleRecovery(recoveryContext, environment, value, tools, candidate, previous)
		}
	}
}

func waitObservation(ctx context.Context) error {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func scheduleRecovery(ctx context.Context, environment *os.Root, value *execution, tools runnerTools, candidate releaseMaterial, previous *releaseMaterial) error {
	if verifyProjectOwnership(ctx, tools, candidate, previous) != nil {
		return writePhase(ctx, environment, value, "needs_attention", "runtime_ownership_unknown")
	}
	if writePhase(ctx, environment, value, "recovering", "candidate_failed") != nil {
		return ErrState
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// The recovery service already exists under its armed absolute timer. This
	// accelerates confirmed failure; the timer remains the independent backstop.
	_, err := runnerCommand(bounded, tools.systemctl, []string{"start", "--no-block", "--", runnerUnitName(*value, true) + ".service"}, nil, nil, 4096)
	return err
}

func executionResponse(request ExecutionRequest, value execution) ExecutionResponse {
	result := ExecutionResponse{SchemaVersion: 1, Action: request.Action, Challenge: request.Challenge, Identity: value.Claim.Identity, ManifestSHA256: value.Claim.ManifestSHA256, FinalSystem: value.Claim.FinalSystem, Phase: value.Phase, Reason: value.Reason}
	if value.Phase == "succeeded" {
		result.ActiveReleaseID = value.Claim.Identity.ReleaseID
	}
	if value.Phase == "recovered" && value.Previous != nil {
		result.ActiveReleaseID = value.Previous.Identity.ReleaseID
	}
	result.RouteRevision = value.RouteReceipt.Revision
	result.RouteConfigSHA256 = value.RouteReceipt.ConfigSHA256
	return result
}

func nativeBootID() (string, error) {
	// #nosec G304 -- Fixed public kernel boot identity; no caller-selected path.
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	value := strings.TrimSpace(string(data))
	if err != nil || !validID(value) {
		return "", ErrState
	}
	return value, nil
}

func RetainedRequest(root *os.Root, environmentID, operationID string) (ExecutionRequest, string, error) {
	if !validID(environmentID) || !validID(operationID) {
		return ExecutionRequest{}, "", ErrInvalid
	}
	environment, err := child(root, environmentID, false)
	if err != nil {
		return ExecutionRequest{}, "", err
	}
	defer func() { _ = environment.Close() }()
	var active claim
	if readRecord(environment, "active.json", &active) != nil || ValidateIdentity(active.Identity) != nil || active.Identity.EnvironmentID != environmentID || active.Identity.OperationID != operationID || !validHash(active.ManifestSHA256) || !finalSystemPath.MatchString(active.FinalSystem) {
		return ExecutionRequest{}, "", ErrState
	}
	return ExecutionRequest{1, "deploy_observe", strings.Repeat("0", 64), active.Identity, active.ManifestSHA256}, active.FinalSystem, nil
}
