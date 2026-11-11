package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"connectrpc.com/connect"
	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/githubflow"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
)

func (h *handler) browser(ctx context.Context) (githubflow.Browser, error) {
	generation := h.sessions.GetInt64(ctx, "generation")
	authenticated, err := h.authentication.Authenticated(ctx, generation)
	if err != nil {
		return githubflow.Browser{}, err
	}
	if !authenticated {
		return githubflow.Browser{}, githubflow.ErrState
	}
	token := h.sessions.Token(ctx)
	if token == "" {
		return githubflow.Browser{}, githubflow.ErrState
	}
	hash := sha256.Sum256([]byte(token))
	return githubflow.Browser{SessionHash: hash[:], Generation: generation}, nil
}
func githubError(err error) error {
	switch {
	case errors.Is(err, githubflow.ErrBusy):
		return safeError(connect.CodeResourceExhausted, "GitHub integration is busy", panelv1.ErrorReason_ERROR_REASON_RATE_LIMITED)
	case errors.Is(err, operations.ErrInvalidIntent):
		return safeError(connect.CodeInvalidArgument, "invalid deployment intent", panelv1.ErrorReason_ERROR_REASON_INVALID_INPUT)
	case errors.Is(err, operations.ErrRequestReused):
		return safeError(connect.CodeAlreadyExists, "request identity already used for different input", panelv1.ErrorReason_ERROR_REASON_REQUEST_ID_REUSED)
	case errors.Is(err, operations.ErrConflict):
		return safeError(connect.CodeFailedPrecondition, "unresolved operation conflicts with deployment", panelv1.ErrorReason_ERROR_REASON_OPERATION_CONFLICT)
	case errors.Is(err, operations.ErrCapacity):
		return safeError(connect.CodeResourceExhausted, "deployment capacity reached", panelv1.ErrorReason_ERROR_REASON_RESOURCE_IN_USE)
	case errors.Is(err, githubflow.ErrInvalid):
		return safeError(connect.CodeInvalidArgument, "invalid GitHub integration input", panelv1.ErrorReason_ERROR_REASON_INVALID_INPUT)
	case errors.Is(err, githubflow.ErrSetup):
		return safeError(connect.CodeFailedPrecondition, "GitHub integration requires setup", panelv1.ErrorReason_ERROR_REASON_INCOMPATIBLE_CONFIGURATION)
	case errors.Is(err, githubflow.ErrOwner), errors.Is(err, githubflow.ErrState):
		return safeError(connect.CodePermissionDenied, "GitHub owner, installation or setup state rejected", panelv1.ErrorReason_ERROR_REASON_AUTHENTICATION_REQUIRED)
	case errors.Is(err, githubflow.ErrProvider):
		return safeError(connect.CodeUnavailable, "GitHub request could not be completed", panelv1.ErrorReason_ERROR_REASON_DEPENDENCY_UNAVAILABLE)
	case errors.Is(err, securestate.ErrStaleVersion):
		return safeError(connect.CodeAborted, "credential version changed", panelv1.ErrorReason_ERROR_REASON_STALE_VERSION)
	default:
		return metadataError(err)
	}
}
func (h *handler) BeginSetup(ctx context.Context, request *connect.Request[panelv1.BeginSetupRequest]) (*connect.Response[panelv1.BeginSetupResponse], error) {
	browser, err := h.browser(ctx)
	if err != nil {
		return nil, githubError(err)
	}
	value, err := h.github.BeginSetup(ctx, request.Msg, browser)
	if err != nil {
		return nil, githubError(err)
	}
	return connect.NewResponse(value), nil
}
func (h *handler) BeginInstallation(ctx context.Context, request *connect.Request[panelv1.BeginInstallationRequest]) (*connect.Response[panelv1.BeginInstallationResponse], error) {
	browser, err := h.browser(ctx)
	if err != nil {
		return nil, githubError(err)
	}
	value, err := h.github.BeginInstallation(ctx, request.Msg, browser)
	if err != nil {
		return nil, githubError(err)
	}
	return connect.NewResponse(value), nil
}
func (h *handler) GetIntegration(ctx context.Context, _ *connect.Request[panelv1.GetIntegrationRequest]) (*connect.Response[panelv1.GetIntegrationResponse], error) {
	value, err := h.github.GetIntegration(ctx)
	if err != nil {
		return nil, githubError(err)
	}
	return connect.NewResponse(value), nil
}
func (h *handler) ListRepositories(ctx context.Context, request *connect.Request[panelv1.ListRepositoriesRequest]) (*connect.Response[panelv1.ListRepositoriesResponse], error) {
	value, err := h.github.ListRepositories(ctx, request.Msg.GetPage())
	if err != nil {
		return nil, githubError(err)
	}
	return connect.NewResponse(value), nil
}
func (h *handler) ListWorkflows(ctx context.Context, request *connect.Request[panelv1.ListWorkflowsRequest]) (*connect.Response[panelv1.ListWorkflowsResponse], error) {
	value, err := h.github.ListWorkflows(ctx, request.Msg.GetRepositoryId(), request.Msg.GetPage())
	if err != nil {
		return nil, githubError(err)
	}
	return connect.NewResponse(value), nil
}
func (h *handler) SelectRepositories(ctx context.Context, request *connect.Request[panelv1.SelectRepositoriesRequest]) (*connect.Response[panelv1.SelectRepositoriesResponse], error) {
	value, err := h.github.SelectRepositories(ctx, request.Msg)
	if err != nil {
		return nil, githubError(err)
	}
	return connect.NewResponse(value), nil
}
func (h *handler) SaveRegistryCredential(ctx context.Context, request *connect.Request[panelv1.SaveRegistryCredentialRequest]) (*connect.Response[panelv1.SaveRegistryCredentialResponse], error) {
	value, err := h.github.SaveRegistryCredential(ctx, request.Msg)
	if err != nil {
		return nil, githubError(err)
	}
	return connect.NewResponse(value), nil
}

// Callbacks are authenticated top-level browser GETs, permitted by SameSite=Lax.
// SCS token hash, live admin generation and one-use provider state supply their
// CSRF boundary; callback errors never echo code/state/provider URLs.
func (h *handler) githubManifest(w http.ResponseWriter, r *http.Request) {
	browser, err := h.browser(r.Context())
	if err != nil {
		http.Error(w, "GitHub setup rejected", http.StatusForbidden)
		return
	}
	query := r.URL.Query()
	if len(query["state"]) != 1 || len(query["code"]) != 1 {
		http.Error(w, "GitHub setup rejected", http.StatusBadRequest)
		return
	}
	destination, err := h.github.CompleteManifest(r.Context(), query.Get("state"), query.Get("code"), browser)
	if err != nil {
		http.Error(w, "GitHub setup failed; restart setup if needed", http.StatusBadRequest)
		return
	}
	parsed, err := url.Parse(destination)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil {
		http.Error(w, "GitHub setup rejected", http.StatusBadRequest)
		return
	}
	// CompleteManifest constructs the fixed GitHub origin plus an escaped App slug.
	http.Redirect(w, r, destination, http.StatusSeeOther) // #nosec G710 -- fixed provider origin rechecked above; never a caller-selected redirect.
}
func (h *handler) githubInstallation(w http.ResponseWriter, r *http.Request) {
	browser, err := h.browser(r.Context())
	if err != nil {
		http.Error(w, "GitHub setup rejected", http.StatusForbidden)
		return
	}
	query := r.URL.Query()
	id, err := strconv.ParseInt(query.Get("installation_id"), 10, 64)
	if err != nil || len(query["state"]) != 1 || len(query["installation_id"]) != 1 {
		http.Error(w, "GitHub setup rejected", http.StatusBadRequest)
		return
	}
	if err := h.github.CompleteInstallation(r.Context(), query.Get("state"), id, browser); err != nil {
		http.Error(w, "GitHub installation rejected; restart installation setup", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
