package executor

import (
	"context"
	"database/sql"
	"encoding/hex"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

type observedService struct {
	Name, Image, ContainerID string
}

type completion struct {
	Result          operations.Result
	ActiveReleaseID string
	Services        []observedService
}

// complete consumes a fresh, challenge-bound native terminal observation only
// after the adapter has verified its route/DNS prerequisites. It atomically
// records the environment, observed services and fenced operation outcome.
// Returning the terminal Result afterwards makes the worker's conditional
// FinishOperation a no-op; errors leave all three unchanged for reconciliation.
func complete(ctx context.Context, database *sql.DB, expected store.Operation, observed completion) error {
	if observed.Result != operations.Succeeded && observed.Result != operations.Recovered {
		return errObservation
	}
	state, outcome, reason := "succeeded", "success", ""
	if observed.Result == operations.Recovered {
		state, outcome, reason = "recovered", "failure", "previous_release_restored"
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	queries := store.New(tx)
	operation, err := queries.GetOperation(ctx, expected.ID)
	if err != nil {
		return err
	}
	if !sameExecution(operation, expected) {
		return errExecutionState
	}
	// A late duplicate must never overwrite a newer environment deployment.
	if operation.State == state && operation.Outcome == outcome {
		return nil
	}
	if operation.State != "running" && operation.State != "recovering" && operation.State != "needs_attention" {
		return errExecutionState
	}
	input, err := queries.GetDeploymentExecution(ctx, operation.ID)
	if err != nil {
		return err
	}
	active := operation.ReleaseID
	if observed.Result == operations.Recovered {
		if !input.PreviousReleaseID.Valid {
			return errObservation
		}
		active = input.PreviousReleaseID.String
	}
	if observed.ActiveReleaseID != active {
		return errObservation
	}
	release, err := executionRelease(ctx, queries, active, operation)
	if err != nil {
		return err
	}
	services, err := queries.ListReleaseServices(ctx, store.ListReleaseServicesParams{ReleaseID: active, Name: "", Limit: 65})
	if err != nil {
		return err
	}
	if !matchesServices(services, observed.Services) {
		return errObservation
	}
	environment, err := queries.GetEnvironment(ctx, operation.EnvironmentID)
	if err != nil {
		return err
	}
	if environment.ServerID != operation.ServerID || environment.CurrentReleaseID != input.PreviousReleaseID {
		return errExecutionState
	}
	if observed.Result == operations.Succeeded {
		previous := environment.CurrentReleaseID
		if previous.String == active {
			previous = environment.PreviousReleaseID
		}
		if err := queries.PromoteEnvironmentRelease(ctx, store.PromoteEnvironmentReleaseParams{CurrentReleaseID: sql.NullString{String: active, Valid: true}, PreviousReleaseID: previous, AppliedVersion: release.EnvironmentVersion, ID: environment.ID}); err != nil {
			return err
		}
	}
	if err := queries.ClearServiceObservations(ctx, environment.ID); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	for _, service := range observed.Services {
		if err := queries.RecordServiceObservation(ctx, store.RecordServiceObservationParams{EnvironmentID: environment.ID, Name: service.Name, ReleaseID: active, OperationID: operation.ID, ImageDigest: service.Image, ContainerID: service.ContainerID, ObservedAt: now}); err != nil {
			return err
		}
	}
	changed, err := queries.FinishOperation(ctx, store.FinishOperationParams{State: state, Outcome: outcome, Reason: reason, UpdatedAt: now, ID: operation.ID, Fence: operation.Fence})
	if err != nil {
		return err
	}
	if changed != 1 {
		return errExecutionState
	}
	return tx.Commit()
}

func matchesServices(expected []store.ReleaseService, observed []observedService) bool {
	if len(expected) == 0 || len(expected) > 64 || len(observed) != len(expected) {
		return false
	}
	images := make(map[string]string, len(expected))
	for _, service := range expected {
		images[service.Name] = service.ImageDigest
	}
	containers := make(map[string]bool, len(observed))
	for _, service := range observed {
		if image, exists := images[service.Name]; !exists || image != service.Image || containers[service.ContainerID] {
			return false
		}
		id, err := hex.DecodeString(service.ContainerID)
		if err != nil || len(id) != 32 || hex.EncodeToString(id) != service.ContainerID {
			return false
		}
		containers[service.ContainerID] = true
		delete(images, service.Name)
	}
	return len(images) == 0
}
