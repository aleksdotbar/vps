package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/deployment"
	"github.com/aleksdotbar/vps/panel/internal/releases"
	"github.com/aleksdotbar/vps/panel/internal/routes"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

// executionInput contains immutable, nonsecret selection data only. Loading it
// is a short transaction; decryption and all remote work happen afterwards.
type executionInput struct {
	operation      store.Operation
	capture        store.DeploymentExecution
	server         store.Server
	management     store.ServerManagement
	installation   store.Installation
	manifest       deployment.Manifest
	manifestSHA256 string
}

func loadInput(ctx context.Context, database *sql.DB, expected store.Operation) (executionInput, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return executionInput{}, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := store.New(tx)
	var input executionInput
	input.operation, err = queries.GetOperation(ctx, expected.ID)
	if err != nil {
		return input, err
	}
	if !sameExecution(input.operation, expected) || (input.operation.State != "running" && input.operation.State != "recovering" && input.operation.State != "needs_attention") {
		return input, errExecutionState
	}
	input.capture, err = queries.GetDeploymentExecution(ctx, expected.ID)
	if err != nil {
		return input, err
	}
	environment, err := queries.GetEnvironment(ctx, expected.EnvironmentID)
	if err != nil {
		return input, err
	}
	if environment.ServerID != expected.ServerID || environment.CurrentReleaseID != input.capture.PreviousReleaseID {
		return input, errExecutionState
	}
	input.server, err = queries.GetServer(ctx, expected.ServerID)
	if err != nil {
		return input, err
	}
	input.management, err = queries.GetServerManagement(ctx, expected.ServerID)
	if err != nil {
		return input, err
	}
	input.installation, err = queries.GetInstallation(ctx)
	if err != nil {
		return input, err
	}
	if input.installation.PrivateProofAt <= 0 || input.management.CredentialKind != "credential" || input.management.CredentialOwnerID != "ssh:"+input.installation.InstallationID+":"+input.server.ID || input.management.CredentialVersion < 1 {
		return input, errExecutionState
	}
	release, err := queries.GetRelease(ctx, expected.ReleaseID)
	if err != nil {
		return input, err
	}
	if release.EnvironmentID != expected.EnvironmentID || release.ServerID != expected.ServerID {
		return input, errExecutionState
	}
	snapshot, err := routes.OperationSnapshot(ctx, queries, expected.ID)
	if err != nil {
		return input, err
	}
	input.manifest, err = releaseManifest(expected, input.capture, input.installation.InstallationID, environment.BridgeName, release, snapshot)
	if err != nil {
		return input, err
	}
	input.manifestSHA256, err = deployment.ManifestSHA256(input.manifest)
	return input, err
}

func releaseManifest(operation store.Operation, captured store.DeploymentExecution, installationID, bridge string, release store.Release, snapshot routes.Snapshot) (deployment.Manifest, error) {
	manifest := deployment.Manifest{
		Identity:           deployment.Identity{InstallationID: installationID, ServerID: operation.ServerID, EnvironmentID: operation.EnvironmentID, OperationID: operation.ID, ReleaseID: operation.ReleaseID, BindingVersion: operation.BindingVersion, ExpectedCurrentReleaseID: captured.PreviousReleaseID.String, ExecutionDeadline: operation.ExecutionDeadline},
		EnvironmentVersion: release.EnvironmentVersion, SecretVersion: release.SecretVersion, ReleaseSHA256: release.ConfigSha256, RouteSnapshotSHA256: snapshot.SHA256, RouteSnapshot: snapshot, BridgeName: bridge, Config: release.ComposeConfig,
		Artifact: releases.Artifact{SchemaVersion: 1, SourceSHA: release.SourceSha, Services: make(map[string]releases.ArtifactService)},
	}
	if len(release.ImagesJson) > releases.MaxArtifactBytes || len(release.DomainsJson) > 64<<10 || len(release.SecretBindingsJson) > 2<<20 || len(release.ComposeConfig) > deployment.MaxApplicationBytes {
		return manifest, errExecutionState
	}
	var images []*panelv1.ServiceImage
	if json.Unmarshal(release.ImagesJson, &images) != nil || len(images) == 0 || len(images) > 64 || json.Unmarshal(release.DomainsJson, &manifest.Domains) != nil || len(manifest.Domains) > 32 || json.Unmarshal(release.SecretBindingsJson, &manifest.Bindings) != nil {
		return manifest, errExecutionState
	}
	for _, image := range images {
		if image == nil {
			return manifest, errExecutionState
		}
		if _, exists := manifest.Artifact.Services[image.Service]; exists {
			return manifest, errExecutionState
		}
		manifest.Artifact.Services[image.Service] = releases.ArtifactService{Image: image.ImageDigest}
	}
	artifact, err := json.Marshal(manifest.Artifact)
	if err != nil {
		return manifest, err
	}
	if _, err := releases.ParseArtifact(artifact, release.SourceSha); err != nil {
		return manifest, errExecutionState
	}
	return manifest, nil
}

func stageRequest(ctx context.Context, vault *securestate.Vault, input executionInput, challenge string) (request deployment.StageRequest, returnErr error) {
	request = deployment.StageRequest{SchemaVersion: 1, Action: "deploy_stage", Challenge: challenge, Manifest: input.manifest, SecretValues: make(map[string][]byte)}
	defer func() {
		if returnErr != nil {
			request.Clear()
		}
	}()
	values, err := vault.Environment(ctx, securestate.RecordRef{Kind: securestate.EnvironmentKind, OwnerID: input.operation.EnvironmentID, Version: input.manifest.SecretVersion})
	if err != nil {
		return request, errors.New("retained environment secrets unavailable")
	}
	defer clear(values)
	for _, binding := range input.manifest.Bindings {
		value, exists := values[binding.Secret]
		if !exists {
			return request, errors.New("retained service secret unavailable")
		}
		if _, copied := request.SecretValues[binding.Secret]; !copied {
			request.SecretValues[binding.Secret] = []byte(value)
		}
	}
	if input.capture.RegistryVersion.Valid {
		token, err := vault.Credential(ctx, securestate.RecordRef{Kind: securestate.CredentialKind, OwnerID: "ghcr-read", Version: input.capture.RegistryVersion.Int64})
		if err != nil {
			return request, errors.New("retained registry credential unavailable")
		}
		request.Registry = &deployment.RegistryCredential{Username: input.capture.RegistryUsername, Token: token}
	}
	// Check encoded secret expansion before creating the transport body. Native
	// staging validates independently; this protects panel memory and payload size.
	materialized, err := deployment.Materialize(request)
	clear(materialized)
	if err != nil {
		return request, errors.New("retained release inputs rejected")
	}
	return request, nil
}
