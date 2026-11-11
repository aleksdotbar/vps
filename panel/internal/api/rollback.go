package api

import (
	"context"

	"connectrpc.com/connect"
	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
)

func (h *handler) RollbackEnvironment(ctx context.Context, request *connect.Request[panelv1.RollbackEnvironmentRequest]) (*connect.Response[panelv1.RollbackEnvironmentResponse], error) {
	if h.builds == nil {
		return nil, safeError(connect.CodeFailedPrecondition, "GitHub build integration is not configured", panelv1.ErrorReason_ERROR_REASON_DEPENDENCY_UNAVAILABLE)
	}
	id, err := h.builds.RequestRollback(ctx, request.Msg)
	if err != nil {
		return nil, githubError(err)
	}
	value, err := h.GetOperation(ctx, connect.NewRequest(&panelv1.GetOperationRequest{Id: id}))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&panelv1.RollbackEnvironmentResponse{Operation: value.Msg.Operation}), nil
}
