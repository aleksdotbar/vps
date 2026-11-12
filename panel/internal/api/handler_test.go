package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"github.com/aleksdotbar/vps/panel/internal/compose"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/riverqueue/river"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/gen/go/panel/v1/panelv1connect"
	"github.com/aleksdotbar/vps/panel/internal/api"
	"github.com/aleksdotbar/vps/panel/internal/auth"
	"github.com/aleksdotbar/vps/panel/internal/cloudflareflow"
	"github.com/aleksdotbar/vps/panel/internal/githubflow"
	"github.com/aleksdotbar/vps/panel/internal/privatefile"
	"github.com/aleksdotbar/vps/panel/internal/projects"
	"github.com/aleksdotbar/vps/panel/internal/registry"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

type installation struct {
	database       *sql.DB
	authentication *auth.Service
	server         *httptest.Server
	client         *http.Client
	origin         string
	cipher         *securestate.Cipher
	runtime        *operations.Runtime
}

func setup(t *testing.T) *installation { return setupProvider(t, nil) }
func setupProvider(t *testing.T, provider http.RoundTripper) *installation {
	return setupConfigured(t, provider, false)
}
func setupConfigured(t *testing.T, provider http.RoundTripper, withBuilds bool) *installation {
	t.Helper()
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "state")
	keyPath := filepath.Join(t.TempDir(), "key", "master")
	if err := auth.Bootstrap(ctx, directory, keyPath, []byte("initial-private-password")); err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(ctx, directory, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	key, err := privatefile.ReadKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := securestate.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	authentication, err := auth.New(ctx, database, cipher)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	origin := "https://" + server.Listener.Addr().String()
	metadata := projects.New(database, cipher)
	integration := githubflow.New(database, cipher, origin, provider)
	var builds *githubflow.Builds
	var runtime *operations.Runtime
	if withBuilds {
		command := os.Getenv("PANEL_TEST_COMPOSE")
		if command == "" {
			command = "docker-compose"
		}
		resolver, err := compose.New(command)
		if err != nil {
			t.Fatal(err)
		}
		runtime, err = operations.New(ctx, database, nil, func(workers *river.Workers, runtime *operations.Runtime) ([]*river.PeriodicJob, error) {
			builds = githubflow.NewBuilds(integration, metadata, resolver, runtime, missingImages{})
			return builds.Register(workers)
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = runtime.Stop(ctx)
		})
	}
	cloudflare := cloudflareflow.New(database, cipher, provider)
	handler, cleanup, err := api.New(database, authentication, origin, metadata, integration, builds, cloudflare, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	server.Config.Handler = handler
	server.StartTLS()
	t.Cleanup(server.Close)
	client := server.Client()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client.Jar = jar
	return &installation{database: database, authentication: authentication, server: server, client: client, origin: origin, cipher: cipher, runtime: runtime}
}
func login(i *installation, password, origin string) (*connect.Response[panelv1.LoginResponse], error) {
	request := connect.NewRequest(&panelv1.LoginRequest{Password: password})
	if origin != "" {
		request.Header().Set("Origin", origin)
	}
	return panelv1connect.NewAuthServiceClient(i.client, i.origin).Login(context.Background(), request)
}
func TestLoginOriginSessionLogoutAndReset(t *testing.T) {
	i := setup(t)
	ctx := context.Background()
	client := panelv1connect.NewAuthServiceClient(i.client, i.origin)
	for _, origin := range []string{"", "https://attacker.example"} {
		_, err := login(i, "initial-private-password", origin)
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("origin %q accepted: %v", origin, err)
		}
	}
	_, err := login(i, "wrong-private-password", i.origin)
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("bad password accepted: %v", err)
	}
	response, err := login(i, "initial-private-password", i.origin)
	if err != nil || !response.Msg.Authenticated {
		t.Fatalf("login failed: %v", err)
	}
	cookies := response.Header().Values("Set-Cookie")
	if len(cookies) != 1 || !strings.Contains(cookies[0], "Secure") || !strings.Contains(cookies[0], "HttpOnly") || !strings.Contains(cookies[0], "SameSite=Lax") {
		t.Fatal("unsafe session cookie")
	}
	current, err := client.GetSession(ctx, connect.NewRequest(&panelv1.GetSessionRequest{}))
	if err != nil || !current.Msg.Authenticated {
		t.Fatalf("persisted SCS session missing: %v", err)
	}
	var persisted int
	if err := i.database.QueryRowContext(ctx, "SELECT count(*) FROM sessions").Scan(&persisted); err != nil || persisted != 1 {
		t.Fatalf("modernc session not persisted: %v", err)
	}
	var storedToken string
	if err := i.database.QueryRowContext(ctx, "SELECT token FROM sessions").Scan(&storedToken); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cookies[0], storedToken) {
		t.Fatal("session bearer token persisted unhashed")
	}
	foreignLogout := connect.NewRequest(&panelv1.LogoutRequest{})
	foreignLogout.Header().Set("Origin", "https://attacker.example")
	if _, err := client.Logout(ctx, foreignLogout); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("foreign logout accepted: %v", err)
	}
	current, err = client.GetSession(ctx, connect.NewRequest(&panelv1.GetSessionRequest{}))
	if err != nil || !current.Msg.Authenticated {
		t.Fatalf("rejected logout changed session: %v", err)
	}
	logout := connect.NewRequest(&panelv1.LogoutRequest{})
	logout.Header().Set("Origin", i.origin)
	if _, err := client.Logout(ctx, logout); err != nil {
		t.Fatal(err)
	}
	current, err = client.GetSession(ctx, connect.NewRequest(&panelv1.GetSessionRequest{}))
	if err != nil || current.Msg.Authenticated {
		t.Fatalf("logout did not invalidate session: %v", err)
	}
	if _, err := login(i, "initial-private-password", i.origin); err != nil {
		t.Fatal(err)
	}
	if err := i.authentication.Reset(ctx, []byte("replacement-private-password")); err != nil {
		t.Fatal(err)
	}
	current, err = client.GetSession(ctx, connect.NewRequest(&panelv1.GetSessionRequest{}))
	if err != nil || current.Msg.Authenticated {
		t.Fatalf("reset session remained valid: %v", err)
	}
	if authenticated, err := i.authentication.Authenticated(ctx, 1); err != nil || authenticated {
		t.Fatalf("old generation accepted after reset: %v", err)
	}
	_, err = login(i, "initial-private-password", i.origin)
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("old password accepted: %v", err)
	}
	if _, err := login(i, "replacement-private-password", i.origin); err != nil {
		t.Fatal(err)
	}
}
func TestPersistentLoginBudgetAndProtectedUnimplementedRPC(t *testing.T) {
	i := setup(t)
	ctx := context.Background()
	panel := panelv1connect.NewPanelServiceClient(i.client, i.origin)
	_, err := panel.GetServer(ctx, connect.NewRequest(&panelv1.GetServerRequest{Id: "a201dc4a-6975-4e68-a6bf-a960b2bfbad1"}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated panel RPC accepted: %v", err)
	}
	for attempt := 0; attempt < 10; attempt++ {
		_, err := login(i, "wrong-private-password", i.origin)
		if connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	_, err = login(i, "initial-private-password", i.origin)
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("login budget bypassed: %v", err)
	}
	var attempts int
	if err := i.database.QueryRowContext(ctx, "SELECT attempts FROM login_budget").Scan(&attempts); err != nil || attempts != 10 {
		t.Fatalf("budget was not durable/bounded: %v", err)
	}
}
func TestWrongKeyFailsBeforeAuthentication(t *testing.T) {
	i := setup(t)
	cipher, err := securestate.NewCipher(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.New(context.Background(), i.database, cipher); !errors.Is(err, securestate.ErrDecrypt) {
		t.Fatalf("wrong key accepted: %v", err)
	}
}

func TestConfiguredOriginRejectsEmptyQueryAndFragmentDelimiters(t *testing.T) {
	for _, origin := range []string{"https://panel.example.ts.net?", "https://panel.example.ts.net#", "https://127.0.0.1:8443?", "https://127.0.0.1:8443#"} {
		_, cleanup, err := api.New(nil, nil, origin, nil, nil, nil, nil, nil)
		if cleanup != nil {
			cleanup()
		}
		if err == nil {
			t.Fatalf("unusable configured origin accepted: %s", origin)
		}
	}
}

// missingImages reports every tag as not yet pushed, keeping deploys waiting.
type missingImages struct{}

func (missingImages) Resolve(context.Context, string, string) (string, error) {
	return "", registry.ErrNotFound
}
