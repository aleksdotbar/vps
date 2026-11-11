package cloudflareflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/mutations"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

const (
	fixtureAccount = "11111111111111111111111111111111"
	fixtureZone    = "22222222222222222222222222222222"
	fixtureTokenID = "33333333333333333333333333333333"
	fixtureZone2   = "44444444444444444444444444444444"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func fixtureService(t *testing.T) *Service {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "state"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	cipher, err := securestate.NewCipher(bytes.Repeat([]byte{17}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return New(database, cipher, nil)
}

func fixtureRequest() *panelv1.ConnectAccountRequest {
	return &panelv1.ConnectAccountRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, AccountId: fixtureAccount, Token: "fixture-one", ZoneIds: []string{fixtureZone}}
}

func providerResponse(t *testing.T, request *http.Request, edit func(string, map[string]any)) *http.Response {
	t.Helper()
	if request.Method != http.MethodGet || request.URL.Host != "api.cloudflare.com" || request.URL.Scheme != "https" || strings.Contains(request.URL.String(), "fixture-one") {
		t.Fatal("provider request escaped read-only fixed-origin credential boundary")
	}
	path := request.URL.Path
	var result any
	switch {
	case path == "/client/v4/accounts/"+fixtureAccount+"/tokens/verify":
		value := map[string]any{"id": fixtureTokenID, "status": "active", "expires_on": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
		if edit != nil {
			edit(path, value)
		}
		result = value
	case path == "/client/v4/accounts/"+fixtureAccount+"/cfd_tunnel" || strings.HasSuffix(path, "/dns_records"):
		if request.URL.Query().Get("per_page") != "1" {
			t.Fatal("unbounded provider list")
		}
		result = []any{}
	case path == "/client/v4/zones/"+fixtureZone || path == "/client/v4/zones/"+fixtureZone2:
		name := "example.com"
		id := fixtureZone
		if strings.HasSuffix(path, fixtureZone2) {
			name, id = "example.org", fixtureZone2
		}
		value := map[string]any{"id": id, "account": map[string]any{"id": fixtureAccount}, "name": name, "status": "active", "type": "full", "paused": false}
		if edit != nil {
			edit(path, value)
		}
		result = value
	default:
		t.Fatalf("unexpected provider path %s", path)
	}
	body, err := json.Marshal(map[string]any{"success": true, "result": result})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)), Request: request}
}

func TestConnectionEncryptsCredentialAndProtectsCanonicalReplay(t *testing.T) {
	service := fixtureService(t)
	calls := 0
	// SDK client environment must not redirect or replace this token.
	t.Setenv("CLOUDFLARE_BASE_URL", "https://untrusted.invalid")
	t.Setenv("CLOUDFLARE_API_TOKEN", "untrusted-token")
	t.Setenv("CLOUDFLARE_API_KEY", "untrusted-key")
	service.transport = fixtureTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Header.Get("Authorization") != "Bearer fixture-one" || request.Header.Get("X-Auth-Key") != "" {
			t.Fatal("ambient credential inherited")
		}
		return providerResponse(t, request, nil), nil
	})
	ctx := context.Background()
	request := fixtureRequest()
	request.ZoneIds = []string{fixtureZone2, fixtureZone}
	response, err := service.ConnectAccount(ctx, request)
	if err != nil || response.GetIntegration().GetVersion() != 1 || len(response.GetIntegration().GetZones()) != 2 || calls != 6 {
		t.Fatalf("connect failed: response=%v calls=%d error=%v", response, calls, err)
	}
	request.ZoneIds = []string{fixtureZone, fixtureZone2}
	replayed, err := service.ConnectAccount(ctx, request)
	if err != nil || !proto.Equal(response, replayed) || calls != 6 {
		t.Fatalf("canonical replay reached provider: calls=%d error=%v", calls, err)
	}
	request.Token = "different-token"
	if _, err := service.ConnectAccount(ctx, request); !errors.Is(err, mutations.ErrReused) || calls != 6 {
		t.Fatalf("changed secret accepted under existing identity: %v", err)
	}
	value, err := service.vault.Credential(ctx, securestate.RecordRef{Kind: securestate.CredentialKind, OwnerID: "cloudflare-api", Version: 1})
	if err != nil || string(value) != "fixture-one" {
		t.Fatalf("encrypted credential unavailable: %v", err)
	}
	row, err := store.New(service.database).GetEncryptedVersion(ctx, store.GetEncryptedVersionParams{Kind: "credential", OwnerID: "cloudflare-api", Version: 1})
	if err != nil || bytes.Contains(row.Ciphertext, value) {
		t.Fatalf("plaintext persisted: %v", err)
	}
	metadata, err := service.GetIntegration(ctx)
	if err != nil || !proto.Equal(metadata.Integration, response.Integration) {
		t.Fatalf("incoherent public metadata: %v", err)
	}
	request = fixtureRequest()
	if _, err := service.ConnectAccount(ctx, request); !errors.Is(err, securestate.ErrStaleVersion) || calls != 6 {
		t.Fatalf("stale version reached provider: %v", err)
	}
}

func TestConnectionRejectsRemoteIdentityAndValidityBeforePersistence(t *testing.T) {
	cases := []struct {
		name string
		edit func(string, map[string]any)
	}{
		{"foreign account", func(path string, value map[string]any) {
			if strings.Contains(path, "/zones/") {
				value["account"] = map[string]any{"id": fixtureZone}
			}
		}},
		{"foreign zone", func(path string, value map[string]any) {
			if strings.Contains(path, "/zones/") {
				value["id"] = fixtureZone2
			}
		}},
		{"inactive token", func(path string, value map[string]any) {
			if strings.HasSuffix(path, "/verify") {
				value["status"] = "disabled"
			}
		}},
		{"expired token", func(path string, value map[string]any) {
			if strings.HasSuffix(path, "/verify") {
				value["expires_on"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
			}
		}},
		{"future token", func(path string, value map[string]any) {
			if strings.HasSuffix(path, "/verify") {
				value["not_before"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
			}
		}},
		{"invalid expiry", func(path string, value map[string]any) {
			if strings.HasSuffix(path, "/verify") {
				value["expires_on"] = "invalid"
			}
		}},
		{"private zone", func(path string, value map[string]any) {
			if strings.Contains(path, "/zones/") {
				value["type"] = "internal"
			}
		}},
		{"inactive zone", func(path string, value map[string]any) {
			if strings.Contains(path, "/zones/") {
				value["status"] = "pending"
			}
		}},
		{"paused zone", func(path string, value map[string]any) {
			if strings.Contains(path, "/zones/") {
				value["paused"] = true
			}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service := fixtureService(t)
			service.transport = fixtureTransport(func(request *http.Request) (*http.Response, error) {
				return providerResponse(t, request, test.edit), nil
			})
			if _, err := service.ConnectAccount(context.Background(), fixtureRequest()); !errors.Is(err, ErrScope) {
				t.Fatalf("untrusted connection accepted: %v", err)
			}
			status, err := service.GetIntegration(context.Background())
			if err != nil || status.GetIntegration().GetConfigured() {
				t.Fatalf("rejected connection persisted: %v", err)
			}
		})
	}
}

func TestProviderVerificationReleasesDatabaseAndRechecksVersion(t *testing.T) {
	service := fixtureService(t)
	ctx := context.Background()
	replaced := false
	service.transport = fixtureTransport(func(request *http.Request) (*http.Response, error) {
		if !replaced {
			replaced = true
			replacement := fixtureRequest()
			replacement.Token = "replacement-token"
			if _, err := service.ConnectAccount(ctx, replacement); err != nil {
				t.Fatalf("provider I/O held SQLite transaction: %v", err)
			}
		}
		return providerResponse(t, request, nil), nil
	})
	if _, err := service.ConnectAccount(ctx, fixtureRequest()); !errors.Is(err, securestate.ErrStaleVersion) {
		t.Fatalf("version change during provider call accepted: %v", err)
	}
	value, err := service.vault.Credential(ctx, securestate.RecordRef{Kind: securestate.CredentialKind, OwnerID: "cloudflare-api", Version: 1})
	if err != nil || string(value) != "replacement-token" {
		t.Fatalf("concurrent credential overwritten: %v", err)
	}
}

func TestConnectionRollsBackCredentialMetadataAndReplayTogether(t *testing.T) {
	service := fixtureService(t)
	ctx := context.Background()
	service.transport = fixtureTransport(func(request *http.Request) (*http.Response, error) { return providerResponse(t, request, nil), nil })
	if _, err := service.database.ExecContext(ctx, `CREATE TRIGGER refuse_zone BEFORE INSERT ON cloudflare_zones BEGIN SELECT RAISE(ABORT, 'fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	request := fixtureRequest()
	if _, err := service.ConnectAccount(ctx, request); err == nil {
		t.Fatal("failed zone write reported success")
	}
	var count int
	if err := service.database.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM encrypted_versions WHERE owner_id='cloudflare-api') + (SELECT count(*) FROM cloudflare_integration) + (SELECT count(*) FROM metadata_mutations)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed transaction persisted partial state: count=%d error=%v", count, err)
	}
	if _, err := service.database.ExecContext(ctx, `DROP TRIGGER refuse_zone`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConnectAccount(ctx, request); err != nil {
		t.Fatalf("failed intent could not retry: %v", err)
	}
}

func TestProviderFailureIsRedactedBoundedAndNotRetried(t *testing.T) {
	service := fixtureService(t)
	calls := 0
	service.transport = fixtureTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"1"}}, Body: io.NopCloser(strings.NewReader(`{"errors":[{"message":"fixture-one"}]}`)), Request: request}, nil
	})
	if _, err := service.ConnectAccount(context.Background(), fixtureRequest()); !errors.Is(err, ErrProvider) || strings.Contains(err.Error(), "fixture-one") || calls != 1 {
		t.Fatalf("provider failure retried or leaked credential: calls=%d error=%v", calls, err)
	}
}

func TestRetainedRouteZoneSurvivesTokenRotationAndRejectsRemoval(t *testing.T) {
	fixture := setupTunnel(t, false)
	service := fixture.tunnels.connection
	queries := store.New(service.database)
	ctx := t.Context()
	if err := queries.UpsertCloudflareZone(ctx, store.UpsertCloudflareZoneParams{ID: fixtureZone, Name: "example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := queries.UpsertCloudflareZone(ctx, store.UpsertCloudflareZoneParams{ID: fixtureZone2, Name: "example.org"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.database.ExecContext(ctx, `UPDATE server_tunnels SET create_attempted=TRUE,create_attempted_at=1,tunnel_id=?,phase='ready',config_sha256=?,observed_at=1`, tunnelFixtureID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	environment, project := uuid.NewString(), uuid.NewString()
	if _, err := service.vault.UpdateEnvironment(ctx, environment, 0, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.database.ExecContext(ctx, `INSERT INTO projects(id,name,repository_id,repository_owner,repository_name,workflow_id,created_at) VALUES(?,'fixture',1,'example','fixture',2,1)`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := service.database.ExecContext(ctx, `INSERT INTO environments(id,project_id,name,server_id,branch,override_path,domains_json,auto_deploy,version,binding_version,secret_version,secret_names_json,bridge_name,created_at) VALUES(?,?,'staging',?,'main','compose.staging.yaml','[]',FALSE,1,1,1,'[]','fixture',1)`, environment, project, tunnelServerID); err != nil {
		t.Fatal(err)
	}
	claimInput := store.CreateRouteClaimParams{Hostname: "app.example.com", ID: uuid.NewString(), EnvironmentID: environment, ServerID: tunnelServerID, InstallationID: tunnelInstallationID, AccountID: fixtureAccount, ZoneID: fixtureZone, ZoneName: "example.com", TunnelID: tunnelFixtureID, CredentialVersion: 1, CreatedAt: 1}
	calls := 0
	service.transport = fixtureTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		// Release admission can claim the old zone while provider verification has
		// released SQLite. The final metadata transaction must recheck ownership.
		if calls == 1 {
			if err := queries.CreateRouteClaim(ctx, claimInput); err != nil {
				t.Fatal(err)
			}
		}
		return providerResponse(t, request, nil), nil
	})
	rejected := fixtureRequest()
	rejected.ExpectedVersion = 1
	rejected.ZoneIds = []string{fixtureZone2}
	if _, err := service.ConnectAccount(ctx, rejected); !errors.Is(err, ErrZoneInUse) || calls == 0 {
		t.Fatalf("ownership change during verification was not rechecked: %v", err)
	}
	verifiedCalls := calls
	rejected.Mutation = &panelv1.Mutation{RequestId: uuid.NewString()}
	if _, err := service.ConnectAccount(ctx, rejected); !errors.Is(err, ErrZoneInUse) || calls != verifiedCalls {
		t.Fatalf("known claimed zone removal reached provider: %v", err)
	}
	unchanged, err := queries.GetCloudflareIntegration(ctx)
	if err != nil || unchanged.Version != 1 {
		t.Fatalf("rejected removal changed credential metadata: %v", err)
	}
	rotated := fixtureRequest()
	rotated.ExpectedVersion = 1
	rotated.Token = "fixture-rotated"
	rotated.ZoneIds = []string{fixtureZone, fixtureZone2}
	response, err := service.ConnectAccount(ctx, rotated)
	if err != nil || response.Integration.Version != 2 {
		t.Fatalf("same-account rotation removed claimed zone: %v", err)
	}
	claim, err := queries.GetRouteClaim(ctx, "app.example.com")
	if err != nil || claim.CredentialVersion != 1 || claim.ZoneID != fixtureZone {
		t.Fatalf("rotation rewrote admitted authority: %v", err)
	}
	dropUnused := fixtureRequest()
	dropUnused.ExpectedVersion = 2
	dropUnused.Token = "fixture-next"
	response, err = service.ConnectAccount(ctx, dropUnused)
	if err != nil || response.Integration.Version != 3 || len(response.Integration.Zones) != 1 {
		t.Fatalf("unclaimed zone could not be removed: %v", err)
	}
}
