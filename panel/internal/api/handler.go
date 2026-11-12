package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"buf.build/go/protovalidate"
	"connectrpc.com/connect"
	"github.com/alexedwards/scs/sqlite3store"
	"github.com/alexedwards/scs/v2"
	"google.golang.org/protobuf/proto"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/gen/go/panel/v1/panelv1connect"
	"github.com/aleksdotbar/vps/panel/internal/auth"
	"github.com/aleksdotbar/vps/panel/internal/cloudflareflow"
	"github.com/aleksdotbar/vps/panel/internal/githubflow"
	"github.com/aleksdotbar/vps/panel/internal/projects"
	"github.com/aleksdotbar/vps/panel/internal/webassets"
)

const maxRPCBytes = 2 << 20

type handler struct {
	panelv1connect.UnimplementedPanelServiceHandler
	panelv1connect.UnimplementedGitHubServiceHandler
	panelv1connect.UnimplementedCloudflareServiceHandler
	database       *sql.DB
	github         *githubflow.Service
	cloudflare     *cloudflareflow.Service
	tunnels        *cloudflareflow.Tunnels
	builds         *githubflow.Builds
	projects       *projects.Service
	sessions       *scs.SessionManager
	authentication *auth.Service
	origin         string
	validator      protovalidate.Validator
}

// New requires an explicit HTTPS origin; proxy headers never choose trust.
// The returned cleanup must run after HTTP requests finish and before DB close.
func New(database *sql.DB, authentication *auth.Service, origin string, metadata *projects.Service, github *githubflow.Service, builds *githubflow.Builds, cloudflare *cloudflareflow.Service, tunnels *cloudflareflow.Tunnels) (http.Handler, func(), error) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || origin != "https://"+parsed.Host {
		return nil, nil, errors.New("origin must be an absolute HTTPS origin without path or credentials")
	}
	validator, err := protovalidate.New()
	if err != nil {
		return nil, nil, errors.New("initialize request validation")
	}
	frontend, err := webassets.New()
	if err != nil {
		return nil, nil, err
	}
	sessionStore := sqlite3store.New(database)
	sessions := scs.New()
	sessions.Store = sessionStore
	sessions.HashTokenInStore = true
	sessions.Lifetime = 12 * time.Hour
	sessions.IdleTimeout = 30 * time.Minute
	sessions.Cookie.Name = "__Host-panel"
	sessions.Cookie.Secure = true
	sessions.Cookie.HttpOnly = true
	// Top-level GitHub setup callbacks need Lax; mutations still require the exact Origin.
	sessions.Cookie.SameSite = http.SameSiteLaxMode
	sessions.Cookie.Path = "/"
	sessions.ErrorFunc = func(w http.ResponseWriter, _ *http.Request, _ error) {
		slog.Error("session storage failed")
		http.Error(w, "session unavailable", http.StatusInternalServerError)
	}
	h := &handler{database: database, github: github, builds: builds, cloudflare: cloudflare, tunnels: tunnels, projects: metadata, sessions: sessions, authentication: authentication, origin: origin, validator: validator}
	options := []connect.HandlerOption{connect.WithReadMaxBytes(maxRPCBytes), connect.WithSendMaxBytes(maxRPCBytes), connect.WithInterceptors(connect.UnaryInterceptorFunc(h.intercept))}
	routing := http.NewServeMux()
	sessionRoute := func(pattern string, handler http.Handler) {
		routing.Handle(pattern, sessions.LoadAndSave(handler))
	}
	sessionRoute(panelv1connect.NewAuthServiceHandler(h, options...))
	sessionRoute(panelv1connect.NewGitHubServiceHandler(h, options...))
	sessionRoute(panelv1connect.NewCloudflareServiceHandler(h, options...))
	sessionRoute("GET /github/manifest", http.HandlerFunc(h.githubManifest))
	sessionRoute("GET /github/installation", http.HandlerFunc(h.githubInstallation))
	sessionRoute(panelv1connect.NewPanelServiceHandler(h, options...))
	routing.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	routing.Handle("/", frontend)
	secured := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' "+webassets.ThemeScriptHash+"; frame-ancestors 'none'; base-uri 'none'; form-action 'self' https://github.com")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		w.Header().Set("Cache-Control", "no-store")
		r.Body = http.MaxBytesReader(w, r.Body, maxRPCBytes)
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		routing.ServeHTTP(w, r.WithContext(ctx))
	})
	return secured, sessionStore.StopCleanup, nil
}

func (h *handler) intercept(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
		procedure := request.Spec().Procedure
		readOnly := request.Spec().IdempotencyLevel == connect.IdempotencyNoSideEffects
		origins := request.Header().Values("Origin")
		if (len(origins) > 0 && (len(origins) != 1 || origins[0] != h.origin)) || (!readOnly && len(origins) != 1) || request.Header().Get("Sec-Fetch-Site") == "cross-site" {
			return nil, safeError(connect.CodePermissionDenied, "origin rejected", panelv1.ErrorReason_ERROR_REASON_ORIGIN_REJECTED)
		}
		if procedure != panelv1connect.AuthServiceLoginProcedure && procedure != panelv1connect.AuthServiceGetSessionProcedure {
			authenticated, err := h.authentication.Authenticated(ctx, h.sessions.GetInt64(ctx, "generation"))
			if err != nil {
				return nil, internalError()
			}
			if !authenticated {
				return nil, safeError(connect.CodeUnauthenticated, "authentication required", panelv1.ErrorReason_ERROR_REASON_AUTHENTICATION_REQUIRED)
			}
		}
		message, ok := request.Any().(proto.Message)
		if !ok || h.validator.Validate(message) != nil {
			return nil, safeError(connect.CodeInvalidArgument, "invalid request", panelv1.ErrorReason_ERROR_REASON_INVALID_INPUT)
		}
		return next(ctx, request)
	}
}

func (h *handler) Login(ctx context.Context, request *connect.Request[panelv1.LoginRequest]) (*connect.Response[panelv1.LoginResponse], error) {
	generation, err := h.authentication.Login(ctx, request.Msg.GetPassword())
	if errors.Is(err, auth.ErrUnauthorized) {
		return nil, safeError(connect.CodeUnauthenticated, "invalid credentials", panelv1.ErrorReason_ERROR_REASON_AUTHENTICATION_REQUIRED)
	}
	if errors.Is(err, auth.ErrLimited) {
		return nil, safeError(connect.CodeResourceExhausted, "login temporarily limited", panelv1.ErrorReason_ERROR_REASON_RATE_LIMITED)
	}
	if err != nil {
		slog.ErrorContext(ctx, "administrator login failed")
		return nil, internalError()
	}
	if err := h.sessions.RenewToken(ctx); err != nil {
		return nil, internalError()
	}
	h.sessions.Put(ctx, "generation", generation)
	return connect.NewResponse(&panelv1.LoginResponse{Authenticated: true}), nil
}
func (h *handler) Logout(ctx context.Context, _ *connect.Request[panelv1.LogoutRequest]) (*connect.Response[panelv1.LogoutResponse], error) {
	if err := h.sessions.Destroy(ctx); err != nil {
		return nil, internalError()
	}
	return connect.NewResponse(&panelv1.LogoutResponse{}), nil
}
func (h *handler) GetSession(ctx context.Context, _ *connect.Request[panelv1.GetSessionRequest]) (*connect.Response[panelv1.GetSessionResponse], error) {
	authenticated, err := h.authentication.Authenticated(ctx, h.sessions.GetInt64(ctx, "generation"))
	if err != nil {
		return nil, internalError()
	}
	return connect.NewResponse(&panelv1.GetSessionResponse{Authenticated: authenticated}), nil
}
func safeError(code connect.Code, message string, reason panelv1.ErrorReason) *connect.Error {
	failure := connect.NewError(code, errors.New(message))
	if detail, err := connect.NewErrorDetail(&panelv1.ErrorDetail{Reason: reason}); err == nil {
		failure.AddDetail(detail)
	}
	return failure
}
func internalError() *connect.Error {
	return connect.NewError(connect.CodeInternal, errors.New("request could not be completed"))
}
