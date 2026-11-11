package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/projects"
	"github.com/aleksdotbar/vps/panel/internal/routes"
)

func (h *handler) CreateProject(ctx context.Context, request *connect.Request[panelv1.CreateProjectRequest]) (*connect.Response[panelv1.CreateProjectResponse], error) {
	if value, found, err := h.projects.ReplayProjectCreation(ctx, request.Msg); err != nil {
		return nil, metadataError(err)
	} else if found {
		return connect.NewResponse(value), nil
	}
	if err := h.github.VerifyProject(ctx, request.Msg); err != nil {
		return nil, githubError(err)
	}
	value, err := h.projects.CreateProject(ctx, request.Msg)
	if err != nil {
		return nil, metadataError(err)
	}
	return connect.NewResponse(value), nil
}
func (h *handler) CreateEnvironment(ctx context.Context, request *connect.Request[panelv1.CreateEnvironmentRequest]) (*connect.Response[panelv1.CreateEnvironmentResponse], error) {
	value, err := h.projects.CreateEnvironment(ctx, request.Msg)
	if err != nil {
		return nil, metadataError(err)
	}
	return connect.NewResponse(value), nil
}
func (h *handler) UpdateEnvironment(ctx context.Context, request *connect.Request[panelv1.UpdateEnvironmentRequest]) (*connect.Response[panelv1.UpdateEnvironmentResponse], error) {
	value, err := h.projects.UpdateEnvironment(ctx, request.Msg)
	if err != nil {
		return nil, metadataError(err)
	}
	return connect.NewResponse(value), nil
}
func (h *handler) SaveEnvironmentSecrets(ctx context.Context, request *connect.Request[panelv1.SaveEnvironmentSecretsRequest]) (*connect.Response[panelv1.SaveEnvironmentSecretsResponse], error) {
	value, err := h.projects.SaveSecrets(ctx, request.Msg)
	if err != nil {
		return nil, metadataError(err)
	}
	return connect.NewResponse(value), nil
}
func (h *handler) GetProject(ctx context.Context, request *connect.Request[panelv1.GetProjectRequest]) (*connect.Response[panelv1.GetProjectResponse], error) {
	value, err := h.projects.GetProject(ctx, request.Msg.GetId())
	if err != nil {
		return nil, metadataError(err)
	}
	return connect.NewResponse(&panelv1.GetProjectResponse{Project: value}), nil
}
func (h *handler) GetEnvironment(ctx context.Context, request *connect.Request[panelv1.GetEnvironmentRequest]) (*connect.Response[panelv1.GetEnvironmentResponse], error) {
	value, err := h.projects.GetEnvironment(ctx, request.Msg.GetId())
	if err != nil {
		return nil, metadataError(err)
	}
	return connect.NewResponse(&panelv1.GetEnvironmentResponse{Environment: value}), nil
}
func (h *handler) GetServer(ctx context.Context, request *connect.Request[panelv1.GetServerRequest]) (*connect.Response[panelv1.GetServerResponse], error) {
	value, err := h.projects.GetServer(ctx, request.Msg.GetId())
	if err != nil {
		return nil, metadataError(err)
	}
	return connect.NewResponse(&panelv1.GetServerResponse{Server: value}), nil
}
func (h *handler) ListProjects(ctx context.Context, request *connect.Request[panelv1.ListProjectsRequest]) (*connect.Response[panelv1.ListProjectsResponse], error) {
	value, err := h.projects.ListProjects(ctx, request.Msg.GetPage())
	if err != nil {
		return nil, metadataError(err)
	}
	return connect.NewResponse(value), nil
}
func (h *handler) ListEnvironments(ctx context.Context, request *connect.Request[panelv1.ListEnvironmentsRequest]) (*connect.Response[panelv1.ListEnvironmentsResponse], error) {
	value, err := h.projects.ListEnvironments(ctx, request.Msg.GetProjectId(), request.Msg.GetPage())
	if err != nil {
		return nil, metadataError(err)
	}
	return connect.NewResponse(value), nil
}
func (h *handler) ListServers(ctx context.Context, request *connect.Request[panelv1.ListServersRequest]) (*connect.Response[panelv1.ListServersResponse], error) {
	value, err := h.projects.ListServers(ctx, request.Msg.GetPage())
	if err != nil {
		return nil, metadataError(err)
	}
	return connect.NewResponse(value), nil
}
func metadataError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return safeError(connect.CodeCanceled, "request canceled", panelv1.ErrorReason_ERROR_REASON_UNSPECIFIED)
	case errors.Is(err, context.DeadlineExceeded):
		return safeError(connect.CodeDeadlineExceeded, "request deadline exceeded", panelv1.ErrorReason_ERROR_REASON_UNSPECIFIED)
	case errors.Is(err, projects.ErrInvalid):
		return safeError(connect.CodeInvalidArgument, "invalid project or environment settings", panelv1.ErrorReason_ERROR_REASON_INVALID_INPUT)
	case errors.Is(err, projects.ErrNotFound):
		return safeError(connect.CodeNotFound, "requested resource not found", panelv1.ErrorReason_ERROR_REASON_NOT_FOUND)
	case errors.Is(err, projects.ErrStale):
		return safeError(connect.CodeAborted, "environment configuration changed", panelv1.ErrorReason_ERROR_REASON_STALE_VERSION)
	case errors.Is(err, projects.ErrReused):
		return safeError(connect.CodeAlreadyExists, "request identity already used for different input", panelv1.ErrorReason_ERROR_REASON_REQUEST_ID_REUSED)
	case errors.Is(err, routes.ErrSetup):
		return safeError(connect.CodeFailedPrecondition, "public routes require a connected account, selected zone and provisioned Tunnel", panelv1.ErrorReason_ERROR_REASON_INCOMPATIBLE_CONFIGURATION)
	case errors.Is(err, routes.ErrOwnership):
		return safeError(connect.CodeAlreadyExists, "hostname ownership conflicts with retained route identity", panelv1.ErrorReason_ERROR_REASON_RESOURCE_IN_USE)
	case errors.Is(err, routes.ErrCapacity):
		return safeError(connect.CodeResourceExhausted, "retained route ownership capacity reached", panelv1.ErrorReason_ERROR_REASON_RESOURCE_IN_USE)
	case errors.Is(err, projects.ErrConflict):
		return safeError(connect.CodeAlreadyExists, "resource is already in use", panelv1.ErrorReason_ERROR_REASON_RESOURCE_IN_USE)
	case errors.Is(err, projects.ErrBusy):
		return safeError(connect.CodeResourceExhausted, "release preparation is busy", panelv1.ErrorReason_ERROR_REASON_RATE_LIMITED)
	case errors.Is(err, projects.ErrCapacity):
		return safeError(connect.CodeResourceExhausted, "metadata capacity reached", panelv1.ErrorReason_ERROR_REASON_RESOURCE_IN_USE)
	case errors.Is(err, projects.ErrBinding):
		return safeError(connect.CodeFailedPrecondition, "deployed environment cannot move to another server", panelv1.ErrorReason_ERROR_REASON_INCOMPATIBLE_CONFIGURATION)
	case errors.Is(err, projects.ErrServer):
		return safeError(connect.CodeFailedPrecondition, "server is not ready for deployment", panelv1.ErrorReason_ERROR_REASON_SERVER_NOT_READY)
	default:
		return internalError()
	}
}

func (h *handler) GetRelease(ctx context.Context, request *connect.Request[panelv1.GetReleaseRequest]) (*connect.Response[panelv1.GetReleaseResponse], error) {
	value, err := h.projects.GetRelease(ctx, request.Msg.GetId())
	if err != nil {
		return nil, metadataError(err)
	}
	return connect.NewResponse(&panelv1.GetReleaseResponse{Release: value}), nil
}
func (h *handler) ListReleases(ctx context.Context, request *connect.Request[panelv1.ListReleasesRequest]) (*connect.Response[panelv1.ListReleasesResponse], error) {
	value, err := h.projects.ListReleases(ctx, request.Msg.GetEnvironmentId(), request.Msg.GetPage())
	if err != nil {
		return nil, metadataError(err)
	}
	return connect.NewResponse(value), nil
}
func (h *handler) ListServices(ctx context.Context, request *connect.Request[panelv1.ListServicesRequest]) (*connect.Response[panelv1.ListServicesResponse], error) {
	value, err := h.projects.ListServices(ctx, request.Msg.GetEnvironmentId(), request.Msg.GetPage())
	if err != nil {
		return nil, metadataError(err)
	}
	return connect.NewResponse(value), nil
}
