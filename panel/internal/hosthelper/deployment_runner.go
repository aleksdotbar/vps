package hosthelper

import (
	"context"
	"os"

	"github.com/aleksdotbar/vps/panel/internal/deployment"
)

func deploymentEffectAuthority(identity deployment.Identity, finalSystem, challenge string, acquire FinalAcquirer) deployment.EffectAuthority {
	return func(ctx context.Context) (func() error, error) {
		if acquire == nil {
			return nil, ErrIdentity
		}
		lease, err := acquire(ctx)
		if err != nil || lease.Release == nil {
			return nil, ErrIdentity
		}
		fail := func() (func() error, error) { _ = lease.Release(); return nil, ErrIdentity }
		if !deploymentLeaseMatches(identity, lease) || lease.FinalSystem != finalSystem {
			return fail()
		}
		request := ReadinessRequest{SchemaVersion: 1, Action: "readiness", InstallationID: identity.InstallationID, ServerID: identity.ServerID, Challenge: challenge}
		observed, err := probe(ProbeRequest{1, "probe", identity.InstallationID, identity.ServerID, challenge})
		if err != nil || !readinessFinalMatches(request, observed, lease) || nativeReadiness(ctx, finalSystem) != nil {
			return fail()
		}
		return func() error {
			after, err := probe(ProbeRequest{1, "probe", identity.InstallationID, identity.ServerID, challenge})
			releaseErr := lease.Release()
			if err != nil || after != observed || releaseErr != nil {
				return ErrIdentity
			}
			return nil
		}, nil
	}
}

// HandleDeploymentExecution runs only fixed control actions over already-staged
// material. Authentication and the 4 KiB canonical input bound remain Serve's
// boundary; native effect authority is reacquired for each short mutation.
func HandleDeploymentExecution(ctx context.Context, request deployment.ExecutionRequest, acquire FinalAcquirer) (deployment.ExecutionResponse, error) {
	before, err := observeSystems()
	if err != nil || before.current != before.profile || before.current != before.booted || deploymentGeneration(request.Identity, request.Challenge, before.current) != nil {
		return deployment.ExecutionResponse{}, ErrIdentity
	}
	root, err := openTunnelDirectory(deploymentDirectory, 0700)
	if err != nil {
		return deployment.ExecutionResponse{}, ErrIdentity
	}
	defer func() { _ = root.Close() }()
	authority := deploymentEffectAuthority(request.Identity, before.current, request.Challenge, acquire)
	var result deployment.ExecutionResponse
	switch request.Action {
	case "deploy_start":
		result, err = deployment.Start(ctx, root, request, before.current, authority)
	case "deploy_observe":
		result, err = deployment.Observe(ctx, root, request, before.current)
	case "deploy_confirm":
		result, err = deployment.Confirm(ctx, root, request, before.current, authority)
	default:
		return deployment.ExecutionResponse{}, ErrRequest
	}
	if err != nil {
		return deployment.ExecutionResponse{}, err
	}
	after, err := observeSystems()
	if err != nil || after != before || deploymentGeneration(request.Identity, request.Challenge, before.current) != nil {
		return deployment.ExecutionResponse{}, ErrIdentity
	}
	return result, nil
}

// RunDeployment is the root-only fixed CLI entrypoint used by the admitted
// transient unit. deployment.Run independently verifies its unit/process proof.
func RunDeployment(ctx context.Context, environmentID, operationID string, recovery bool, acquire FinalAcquirer) error {
	if os.Geteuid() != 0 {
		return ErrPrivilege
	}
	root, err := openTunnelDirectory(deploymentDirectory, 0700)
	if err != nil {
		return ErrIdentity
	}
	defer func() { _ = root.Close() }()
	request, finalSystem, err := deployment.RetainedRequest(root, environmentID, operationID)
	if err != nil {
		return err
	}
	authority := deploymentEffectAuthority(request.Identity, finalSystem, request.Challenge, acquire)
	return deployment.Run(ctx, root, environmentID, operationID, finalSystem, recovery, authority)
}

// RunDeploymentRegistryCleanup is the fixed root-only expiry CLI. It removes
// only elapsed, ownership-bound private registry files, even if staging never
// published its durable application receipt. It never contacts Docker.
func RunDeploymentRegistryCleanup(ctx context.Context, operationID string) error {
	if os.Geteuid() != 0 {
		return ErrPrivilege
	}
	return deployment.CleanupExpiredRegistry(ctx, operationID)
}
