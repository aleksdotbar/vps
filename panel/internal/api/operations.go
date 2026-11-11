package api

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

func (h *handler) GetOperation(ctx context.Context, request *connect.Request[panelv1.GetOperationRequest]) (*connect.Response[panelv1.GetOperationResponse], error) {
	id, err := uuid.Parse(request.Msg.GetId())
	if err != nil {
		return nil, safeError(connect.CodeInvalidArgument, "invalid operation identity", panelv1.ErrorReason_ERROR_REASON_INVALID_INPUT)
	}
	value, err := store.New(h.database).GetOperationHistory(ctx, id.String())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, safeError(connect.CodeNotFound, "operation not found", panelv1.ErrorReason_ERROR_REASON_NOT_FOUND)
	}
	if err != nil {
		return nil, metadataError(err)
	}
	return connect.NewResponse(&panelv1.GetOperationResponse{Operation: historyOperation(value)}), nil
}

func operationMessage(operation store.Operation) *panelv1.Operation {
	kinds := map[string]panelv1.OperationKind{"enroll": panelv1.OperationKind_OPERATION_KIND_ENROLL_SERVER, "deploy": panelv1.OperationKind_OPERATION_KIND_DEPLOY, "rollback": panelv1.OperationKind_OPERATION_KIND_ROLLBACK}
	states := map[string]panelv1.OperationState{"queued": panelv1.OperationState_OPERATION_STATE_QUEUED, "running": panelv1.OperationState_OPERATION_STATE_RUNNING, "succeeded": panelv1.OperationState_OPERATION_STATE_SUCCEEDED, "failed": panelv1.OperationState_OPERATION_STATE_FAILED, "recovering": panelv1.OperationState_OPERATION_STATE_RECOVERING, "recovered": panelv1.OperationState_OPERATION_STATE_RECOVERED, "superseded": panelv1.OperationState_OPERATION_STATE_SUPERSEDED, "needs_attention": panelv1.OperationState_OPERATION_STATE_NEEDS_ATTENTION}
	outcomes := map[string]panelv1.OperationOutcome{"pending": panelv1.OperationOutcome_OPERATION_OUTCOME_PENDING, "success": panelv1.OperationOutcome_OPERATION_OUTCOME_CONFIRMED_SUCCESS, "failure": panelv1.OperationOutcome_OPERATION_OUTCOME_CONFIRMED_FAILURE, "partial": panelv1.OperationOutcome_OPERATION_OUTCOME_PARTIAL, "unknown": panelv1.OperationOutcome_OPERATION_OUTCOME_UNKNOWN}
	messages := map[string]string{"remote_running": "Deployment is running.", "recovery_running": "Previous release recovery is running.", "execution_deadline_exceeded": "Operation deadline exceeded; remote outcome requires attention.", "confirmed_failure": "Operation failed.", "previous_release_restored": "Previous release restored.", "executor_unavailable": "Operation executor is not available.", "reconciliation_exhausted": "Deployment attempts stopped; outcome remains unconfirmed.", "partial_application": "Operation partially applied; reconciliation pending.", "remote_outcome_unknown": "Remote outcome is unknown; reconciliation pending.", "superseded_by_rollback": "Queued deployment superseded by rollback.", "automatic_deploy_disabled": "Queued automatic deployment disabled.", "binding_changed": "Queued operation superseded because environment binding changed.", "newer_build": "Queued automatic deployment superseded by a newer build.", "superseded_by_manual": "Queued automatic deployment superseded by explicit deployment."}
	return &panelv1.Operation{Id: operation.ID, Kind: kinds[operation.Kind], State: states[operation.State], ServerId: operation.ServerID, EnvironmentId: operation.EnvironmentID, ReleaseId: operation.ReleaseID, CreatedAt: timestamppb.New(time.UnixMilli(operation.CreatedAt)), UpdatedAt: timestamppb.New(time.UnixMilli(operation.UpdatedAt)), Message: messages[operation.Reason], Outcome: outcomes[operation.Outcome]}
}

func waitingOperation(value store.WaitingBuild) *panelv1.Operation {
	states := map[string]panelv1.OperationState{"waiting": panelv1.OperationState_OPERATION_STATE_WAITING_FOR_BUILD, "dispatch_unknown": panelv1.OperationState_OPERATION_STATE_WAITING_FOR_BUILD, "queued": panelv1.OperationState_OPERATION_STATE_QUEUED, "failed": panelv1.OperationState_OPERATION_STATE_FAILED, "superseded": panelv1.OperationState_OPERATION_STATE_SUPERSEDED, "needs_attention": panelv1.OperationState_OPERATION_STATE_NEEDS_ATTENTION}
	messages := map[string]string{"": "Waiting for a verified GitHub build.", "discovery_limit": "Exact build search exceeded the supported history window.", "dispatch_outcome_unknown": "Build dispatch outcome is unknown; submit a fresh explicit deployment to retry safely.", "build_timeout": "Build wait deadline exceeded.", "polling_exhausted": "Build polling exhausted; submit a fresh explicit deployment.", "incompatible_build": "Build artifact or Compose configuration is incompatible.", "route_ownership_conflict": "Captured hostname ownership conflicts with another environment or retained route identity.", "route_setup_required": "Public routes require a connected account, selected zone and provisioned server Tunnel.", "route_capacity": "Retained hostname ownership capacity reached.", "provider_rejected": "GitHub rejected the build request; check installation and workflow access.", "build_failed": "GitHub build failed.", "branch_head_changed": "Selected source is no longer branch head; no build was dispatched.", "untrusted_build": "GitHub build identity did not match the selected source.", "manual_intent": "Build request superseded by explicit deployment.", "newer_build": "Build request superseded by a newer successful build.", "binding_changed": "Build request superseded because environment binding changed.", "automatic_deploy_disabled": "Automatic build request superseded because automation was disabled."}
	outcome := panelv1.OperationOutcome_OPERATION_OUTCOME_PENDING
	if value.State == "failed" {
		outcome = panelv1.OperationOutcome_OPERATION_OUTCOME_CONFIRMED_FAILURE
	}
	if value.State == "needs_attention" {
		outcome = panelv1.OperationOutcome_OPERATION_OUTCOME_UNKNOWN
	}
	return &panelv1.Operation{Id: value.ID, Kind: panelv1.OperationKind_OPERATION_KIND_DEPLOY, State: states[value.State], ServerId: value.ServerID, EnvironmentId: value.EnvironmentID, CreatedAt: timestamppb.New(time.UnixMilli(value.CreatedAt)), UpdatedAt: timestamppb.New(time.UnixMilli(value.UpdatedAt)), Message: messages[value.Reason], Outcome: outcome}
}
func (h *handler) DeployEnvironment(ctx context.Context, request *connect.Request[panelv1.DeployEnvironmentRequest]) (*connect.Response[panelv1.DeployEnvironmentResponse], error) {
	if h.builds == nil {
		return nil, safeError(connect.CodeFailedPrecondition, "GitHub build integration is not configured", panelv1.ErrorReason_ERROR_REASON_DEPENDENCY_UNAVAILABLE)
	}
	id, err := h.builds.RequestDeploy(ctx, request.Msg)
	if err != nil {
		return nil, githubError(err)
	}
	value, err := h.GetOperation(ctx, connect.NewRequest(&panelv1.GetOperationRequest{Id: id}))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&panelv1.DeployEnvironmentResponse{Operation: value.Msg.Operation}), nil
}

func (h *handler) GetDeploymentSource(ctx context.Context, request *connect.Request[panelv1.GetDeploymentSourceRequest]) (*connect.Response[panelv1.GetDeploymentSourceResponse], error) {
	if h.builds == nil {
		return nil, safeError(connect.CodeFailedPrecondition, "GitHub build integration is not configured", panelv1.ErrorReason_ERROR_REASON_DEPENDENCY_UNAVAILABLE)
	}
	value, err := h.builds.GetDeploymentSource(ctx, request.Msg.GetEnvironmentId(), request.Msg.GetExpectedVersion())
	if err != nil {
		return nil, githubError(err)
	}
	return connect.NewResponse(value), nil
}
