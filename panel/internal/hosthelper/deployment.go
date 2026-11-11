package hosthelper

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/aleksdotbar/vps/panel/internal/deployment"
)

const deploymentDirectory = "/var/lib/panel-deploy"

func handleDeploymentStage(ctx context.Context, request deployment.StageRequest, acquire FinalAcquirer) (response deployment.Response, returnErr error) {
	if acquire == nil {
		return deployment.Response{}, ErrIdentity
	}
	lease, err := acquire(ctx)
	if err != nil || lease.Release == nil {
		return deployment.Response{}, ErrIdentity
	}
	defer func() {
		if lease.Release() != nil {
			response = deployment.Response{}
			returnErr = ErrIdentity
		}
	}()
	identity := request.Manifest.Identity
	if !deploymentLeaseMatches(identity, lease) || deploymentGeneration(identity, request.Challenge, lease.FinalSystem) != nil {
		return deployment.Response{}, ErrIdentity
	}
	parent, err := os.OpenRoot("/var/lib")
	if err != nil {
		return deployment.Response{}, ErrIdentity
	}
	defer func() { _ = parent.Close() }()
	if err := parent.Mkdir("panel-deploy", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return deployment.Response{}, ErrIdentity
	}
	if syncTunnelDirectory(parent) != nil {
		return deployment.Response{}, ErrIdentity
	}
	root, err := openTunnelDirectory(deploymentDirectory, 0700)
	if err != nil {
		return deployment.Response{}, ErrIdentity
	}
	defer func() { _ = root.Close() }()
	response, err = deployment.Stage(ctx, root, request, lease.FinalSystem)
	if err != nil {
		return deployment.Response{}, err
	}
	if deploymentGeneration(identity, request.Challenge, lease.FinalSystem) != nil {
		return deployment.Response{}, ErrIdentity
	}
	return response, nil
}

func deploymentLeaseMatches(identity deployment.Identity, lease FinalLease) bool {
	return lease.Release != nil && lease.InstallationID == identity.InstallationID && lease.ServerID == identity.ServerID && systemPath.MatchString(lease.FinalSystem)
}

func deploymentGeneration(identity deployment.Identity, challenge, expected string) error {
	observed, err := probe(ProbeRequest{1, "probe", identity.InstallationID, identity.ServerID, challenge})
	if err != nil || observed.Stage != "final" || observed.CurrentSystem != expected || observed.SystemProfile != expected {
		return ErrIdentity
	}
	return nil
}

func handleDeploymentStatus(request deployment.StatusRequest) (deployment.Response, error) {
	before, err := observeSystems()
	if err != nil || before.current != before.profile {
		return deployment.Response{}, ErrIdentity
	}
	if deploymentGeneration(request.Identity, request.Challenge, before.current) != nil {
		return deployment.Response{}, ErrIdentity
	}
	root, err := openTunnelDirectory(deploymentDirectory, 0700)
	if err != nil {
		return deployment.Response{}, ErrIdentity
	}
	defer func() { _ = root.Close() }()
	response, err := deployment.Status(root, request, before.current)
	if err != nil {
		return deployment.Response{}, err
	}
	if deploymentGeneration(request.Identity, request.Challenge, before.current) != nil {
		return deployment.Response{}, ErrIdentity
	}
	return response, nil
}

func encodeDeploymentResponse(response deployment.Response) ([]byte, error) {
	data, err := json.Marshal(response)
	if err != nil || len(data) > deployment.MaxStatusBytes {
		return nil, ErrIO
	}
	return data, nil
}
