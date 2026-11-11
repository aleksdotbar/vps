// Package executor coordinates panel-owned deployment state and native receipts.
// These transactions never perform network or target effects.
package executor

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/store"
)

var errExecutionState = errors.New("deployment execution identity or current release changed")
var errObservation = errors.New("deployment observation does not match retained release")

// capture fixes the previous release and registry credential reference before
// any target effect. Retries never select a newly edited credential or baseline.
func capture(ctx context.Context, database *sql.DB, expected store.Operation) (store.DeploymentExecution, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return store.DeploymentExecution{}, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := store.New(tx)
	operation, err := queries.GetOperation(ctx, expected.ID)
	if err != nil {
		return store.DeploymentExecution{}, err
	}
	if !sameExecution(operation, expected) || (operation.State != "running" && operation.State != "recovering") || operation.ExecutionDeadline <= time.Now().UnixMilli() {
		return store.DeploymentExecution{}, errExecutionState
	}
	retained, err := queries.GetDeploymentExecution(ctx, operation.ID)
	if err == nil {
		return retained, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return store.DeploymentExecution{}, err
	}
	environment, err := queries.GetEnvironment(ctx, operation.EnvironmentID)
	if err != nil {
		return store.DeploymentExecution{}, err
	}
	if environment.ServerID != operation.ServerID {
		return store.DeploymentExecution{}, errExecutionState
	}
	if _, err := executionRelease(ctx, queries, operation.ReleaseID, operation); err != nil {
		return store.DeploymentExecution{}, err
	}
	if environment.CurrentReleaseID.Valid {
		if _, err := executionRelease(ctx, queries, environment.CurrentReleaseID.String, operation); err != nil {
			return store.DeploymentExecution{}, err
		}
	}
	registry, err := queries.GetRegistryCredential(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return store.DeploymentExecution{}, err
	}
	version := sql.NullInt64{}
	if err == nil {
		if registry.Version < 1 || registry.Username == "" {
			return store.DeploymentExecution{}, errExecutionState
		}
		version = sql.NullInt64{Int64: registry.Version, Valid: true}
	}
	if err := queries.CaptureDeploymentExecution(ctx, store.CaptureDeploymentExecutionParams{OperationID: operation.ID, PreviousReleaseID: environment.CurrentReleaseID, RegistryUsername: registry.Username, RegistryVersion: version, CreatedAt: time.Now().UnixMilli()}); err != nil {
		return store.DeploymentExecution{}, err
	}
	retained, err = queries.GetDeploymentExecution(ctx, operation.ID)
	if err != nil {
		return store.DeploymentExecution{}, err
	}
	if err := tx.Commit(); err != nil {
		return store.DeploymentExecution{}, err
	}
	return retained, nil
}

func sameExecution(actual, expected store.Operation) bool {
	return actual.ID == expected.ID && actual.Started && actual.Fence == expected.Fence &&
		(actual.Kind == "deploy" || actual.Kind == "rollback") && actual.Kind == expected.Kind &&
		actual.EnvironmentID == expected.EnvironmentID && actual.ServerID == expected.ServerID &&
		actual.ReleaseID == expected.ReleaseID && actual.BindingVersion == expected.BindingVersion &&
		actual.ExecutionDeadline > 0 && actual.ExecutionDeadline == expected.ExecutionDeadline
}

func executionRelease(ctx context.Context, queries *store.Queries, id string, operation store.Operation) (store.GetReleaseMetadataRow, error) {
	release, err := queries.GetReleaseMetadata(ctx, id)
	if err != nil {
		return store.GetReleaseMetadataRow{}, err
	}
	if release.EnvironmentID != operation.EnvironmentID || release.ServerID != operation.ServerID {
		return store.GetReleaseMetadataRow{}, errExecutionState
	}
	return release, nil
}
