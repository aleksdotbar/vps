package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/cloudflareflow"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
)

func cloudflareError(err error) error {
	switch {
	case errors.Is(err, operations.ErrCapacity):
		return safeError(connect.CodeResourceExhausted, "Tunnel provisioning capacity reached", panelv1.ErrorReason_ERROR_REASON_RESOURCE_IN_USE)
	case errors.Is(err, cloudflareflow.ErrZoneInUse):
		return safeError(connect.CodeFailedPrecondition, "selected zone owns retained route claims", panelv1.ErrorReason_ERROR_REASON_RESOURCE_IN_USE)
	case errors.Is(err, cloudflareflow.ErrAccountInUse), errors.Is(err, cloudflareflow.ErrTunnelConflict):
		return safeError(connect.CodeFailedPrecondition, "Cloudflare account or server owns existing Tunnel state", panelv1.ErrorReason_ERROR_REASON_RESOURCE_IN_USE)
	case errors.Is(err, cloudflareflow.ErrHostSetup):
		return safeError(connect.CodeFailedPrecondition, "verified final host management is required", panelv1.ErrorReason_ERROR_REASON_SERVER_NOT_READY)
	case errors.Is(err, cloudflareflow.ErrInvalid):
		return safeError(connect.CodeInvalidArgument, "invalid Cloudflare connection input", panelv1.ErrorReason_ERROR_REASON_INVALID_INPUT)
	case errors.Is(err, cloudflareflow.ErrScope):
		return safeError(connect.CodePermissionDenied, "Cloudflare token or selected zone rejected", panelv1.ErrorReason_ERROR_REASON_AUTHENTICATION_REQUIRED)
	case errors.Is(err, cloudflareflow.ErrProvider):
		return safeError(connect.CodeUnavailable, "Cloudflare verification could not be completed", panelv1.ErrorReason_ERROR_REASON_DEPENDENCY_UNAVAILABLE)
	case errors.Is(err, cloudflareflow.ErrBusy):
		return safeError(connect.CodeResourceExhausted, "Cloudflare connection is busy", panelv1.ErrorReason_ERROR_REASON_RATE_LIMITED)
	case errors.Is(err, securestate.ErrStaleVersion):
		return safeError(connect.CodeAborted, "Cloudflare connection version changed", panelv1.ErrorReason_ERROR_REASON_STALE_VERSION)
	default:
		return metadataError(err)
	}
}

func (h *handler) ConnectAccount(ctx context.Context, request *connect.Request[panelv1.ConnectAccountRequest]) (*connect.Response[panelv1.ConnectAccountResponse], error) {
	value, err := h.cloudflare.ConnectAccount(ctx, request.Msg)
	if err != nil {
		return nil, cloudflareError(err)
	}
	return connect.NewResponse(value), nil
}

func (h *handler) GetCloudflareIntegration(ctx context.Context, _ *connect.Request[panelv1.GetCloudflareIntegrationRequest]) (*connect.Response[panelv1.GetCloudflareIntegrationResponse], error) {
	value, err := h.cloudflare.GetIntegration(ctx)
	if err != nil {
		return nil, cloudflareError(err)
	}
	return connect.NewResponse(value), nil
}

func (h *handler) ProvisionServerTunnel(ctx context.Context, request *connect.Request[panelv1.ProvisionServerTunnelRequest]) (*connect.Response[panelv1.ProvisionServerTunnelResponse], error) {
	if h.tunnels == nil {
		return nil, cloudflareError(cloudflareflow.ErrHostSetup)
	}
	value, err := h.tunnels.Provision(ctx, request.Msg)
	if err != nil {
		return nil, cloudflareError(err)
	}
	return connect.NewResponse(value), nil
}
func (h *handler) GetServerTunnel(ctx context.Context, request *connect.Request[panelv1.GetServerTunnelRequest]) (*connect.Response[panelv1.GetServerTunnelResponse], error) {
	if h.tunnels == nil {
		return nil, cloudflareError(cloudflareflow.ErrHostSetup)
	}
	value, err := h.tunnels.Status(ctx, request.Msg.GetServerId())
	if err != nil {
		return nil, cloudflareError(err)
	}
	return connect.NewResponse(value), nil
}
