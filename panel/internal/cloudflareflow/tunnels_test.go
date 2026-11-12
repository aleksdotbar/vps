package cloudflareflow

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"golang.org/x/crypto/ssh"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/hosthelper"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/remote"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

const tunnelServerID = "55555555-5555-4555-8555-555555555555"
const tunnelInstallationID = "66666666-6666-4666-8666-666666666666"
const tunnelFixtureID = "77777777-7777-4777-8777-777777777777"

type fixtureHelper func(context.Context, remote.Target, []byte, []byte) (remote.Result, error)

func (f fixtureHelper) Helper(ctx context.Context, target remote.Target, key, payload []byte) (remote.Result, error) {
	return f(ctx, target, key, payload)
}

type tunnelFixture struct {
	tunnels                    *Tunnels
	row                        store.ServerTunnel
	calls, posts               int
	created, ready, deny, lose bool
	finalized                  bool
}

func setupTunnel(t *testing.T, admit bool) *tunnelFixture {
	t.Helper()
	service := fixtureService(t)
	ctx := t.Context()
	management := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{41}, 32))
	host := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{42}, 32))
	public, err := ssh.NewPublicKey(host.Public())
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(management)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(management, "")
	if err != nil {
		t.Fatal(err)
	}
	owner := "ssh:" + tunnelInstallationID + ":" + tunnelServerID
	if _, err := service.vault.SaveCredential(ctx, owner, 0, pem.EncodeToMemory(block)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.vault.SaveCredential(ctx, "cloudflare-api", 0, []byte("fixture-management-token")); err != nil {
		t.Fatal(err)
	}
	if err := store.New(service.database).SetCloudflareIntegration(ctx, store.SetCloudflareIntegrationParams{AccountID: fixtureAccount, TokenID: fixtureTokenID, Version: 1, VerifiedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.database.ExecContext(ctx, `INSERT INTO servers(id,name,tailscale_address,ssh_fingerprint,panel_host,readiness,created_at) VALUES(?,?,?,?,TRUE,'setup_pending',1)`, tunnelServerID, "fixture", "100.64.0.1", ssh.FingerprintSHA256(public)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.database.ExecContext(ctx, `INSERT INTO server_management(server_id,credential_kind,credential_owner_id,credential_version,host_public_key,management_fingerprint) VALUES(?,'credential',?,1,?,?)`, tunnelServerID, owner, string(ssh.MarshalAuthorizedKey(public)), ssh.FingerprintSHA256(signer.PublicKey())); err != nil {
		t.Fatal(err)
	}
	if _, err := service.database.ExecContext(ctx, `INSERT INTO installation(id,installation_id,panel_server_id,bootstrap_identity,private_proof_at,created_at) VALUES(1,?,?,?,1,1)`, tunnelInstallationID, tunnelServerID, bytes.Repeat([]byte{1}, 32)); err != nil {
		t.Fatal(err)
	}
	result := &tunnelFixture{}
	if admit {
		runtime, err := operations.New(ctx, service.database, nil, func(workers *river.Workers, runtime *operations.Runtime) ([]*river.PeriodicJob, error) {
			result.tunnels = NewTunnels(service, nil, runtime)
			return nil, result.tunnels.Register(workers)
		})
		if err != nil {
			t.Fatal(err)
		}
		result.tunnels.runtime = runtime
	} else {
		result.tunnels = NewTunnels(service, nil, nil)
	}
	requestID := uuid.NewString()
	result.tunnels.remote = fixtureHelper(func(ctx context.Context, _ remote.Target, _ []byte, payload []byte) (remote.Result, error) {
		if result.deny {
			return remote.Result{}, remote.ErrRemoteExit
		}
		request, err := hosthelper.ParseTunnelRequest(payload)
		if err != nil {
			t.Fatal(err)
		}
		response := hosthelper.TunnelResponse{SchemaVersion: 1, Action: request.Action, InstallationID: request.InstallationID, ServerID: request.ServerID, OperationID: request.OperationID, Challenge: request.Challenge, AccountID: request.AccountID, Name: request.Name, Phase: "prepared"}
		if request.Action == "tunnel_prepare" {
			response.Secret = bytes.Repeat([]byte{7}, 32)
		}
		if request.Action == "tunnel_finalize" {
			result.finalized = true
		}
		if result.finalized {
			response.TunnelID = tunnelFixtureID
			response.Phase = "finalizing"
			response.Secret = nil
			if result.ready {
				response.Phase = "ready"
				response.CredentialProof = true
				response.ConfigSHA256 = strings.Repeat("a", 64)
			}
		}
		// #nosec G117 -- Synthetic fixture for the SSH-only secret relay; never an HTTP response or log.
		data, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		return remote.Result{Outcome: remote.CommandExited, ExitCode: 0, Output: data}, nil
	})
	service.transport = fixtureTransport(func(request *http.Request) (*http.Response, error) {
		result.calls++
		if request.Method == http.MethodPost {
			result.posts++
			var input struct{ Name, ConfigSrc, TunnelSecret string }
			var data map[string]string
			if json.NewDecoder(request.Body).Decode(&data) != nil {
				t.Fatal("invalid create payload")
			}
			input.Name, input.ConfigSrc, input.TunnelSecret = data["name"], data["config_src"], data["tunnel_secret"]
			if input.Name != result.row.Name || input.ConfigSrc != "local" || input.TunnelSecret != base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)) {
				t.Fatal("create payload lost prepared identity/secret")
			}
			result.created = true
			if result.lose {
				return nil, errors.New("lost fixture-management-token response")
			}
		}
		tunnel := map[string]any{"id": tunnelFixtureID, "account_tag": fixtureAccount, "name": result.row.Name, "config_src": "local", "tun_type": "cfd_tunnel", "created_at": time.Now().UTC().Format(time.RFC3339), "deleted_at": nil}
		var value any = tunnel
		if request.Method == http.MethodGet && !strings.HasSuffix(request.URL.Path, tunnelFixtureID) {
			value = []any{}
			if result.created {
				value = []any{tunnel}
			}
			if request.URL.Query().Get("name") != result.row.Name || request.URL.Query().Get("per_page") != "2" {
				t.Fatal("unbounded/ambiguous reconciliation query")
			}
		}
		body, err := json.Marshal(map[string]any{"success": true, "result": value})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
	})
	if !admit {
		now := time.Now().UnixMilli()
		if err := store.New(service.database).CreateServerTunnel(ctx, store.CreateServerTunnelParams{ServerID: tunnelServerID, RequestID: requestID, InstallationID: tunnelInstallationID, AccountID: fixtureAccount, CredentialVersion: 1, Name: "panel-" + tunnelServerID + "-" + requestID, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		result.row, err = store.New(service.database).GetServerTunnel(ctx, tunnelServerID)
		if err != nil {
			t.Fatal(err)
		}
	}
	return result
}
func TestTunnelLostCreateResponseReconcilesWithoutSecondPOSTAndRequiresCredentialProof(t *testing.T) {
	fixture := setupTunnel(t, false)
	fixture.lose = true
	if delay, err := fixture.tunnels.advance(t.Context(), tunnelServerID); err != nil || delay == 0 {
		t.Fatalf("unknown create lost recovery: delay=%v error=%v", delay, err)
	}
	if fixture.posts != 1 {
		t.Fatalf("create count=%d", fixture.posts)
	}
	row, err := store.New(fixture.tunnels.connection.database).GetServerTunnel(t.Context(), tunnelServerID)
	if err != nil || !row.CreateAttempted || row.TunnelID.Valid {
		t.Fatalf("unknown effect lacks durable evidence: %v", err)
	}
	// Reconstruct the process-facing coordinator against the same durable state.
	restarted := NewTunnels(fixture.tunnels.connection, nil, nil)
	restarted.remote = fixture.tunnels.remote
	fixture.tunnels = restarted
	if delay, err := fixture.tunnels.advance(t.Context(), tunnelServerID); err != nil || delay == 0 || fixture.posts != 1 {
		t.Fatalf("reconciliation retried POST or accepted name-only proof: posts=%d delay=%v error=%v", fixture.posts, delay, err)
	}
	fixture.ready = true
	if delay, err := fixture.tunnels.advance(t.Context(), tunnelServerID); err != nil || delay != 0 || fixture.posts != 1 {
		t.Fatalf("credential-proven reconciliation failed: posts=%d delay=%v error=%v", fixture.posts, delay, err)
	}
	row, err = store.New(fixture.tunnels.connection.database).GetServerTunnel(t.Context(), tunnelServerID)
	if err != nil || row.Phase != "ready" || row.ConfigSha256 == "" || row.ObservedAt == 0 {
		t.Fatalf("proof not recorded: %v", err)
	}
}
func TestTunnelRestartReplaysIncompleteTargetFinalizationWithoutAnotherCreate(t *testing.T) {
	fixture := setupTunnel(t, false)
	original := fixture.tunnels.remote
	finalizations := 0
	fixture.tunnels.remote = fixtureHelper(func(ctx context.Context, target remote.Target, key, payload []byte) (remote.Result, error) {
		request, err := hosthelper.ParseTunnelRequest(payload)
		if err != nil {
			return remote.Result{}, err
		}
		if request.Action == "tunnel_finalize" {
			finalizations++
			// The first attempt left a durable final receipt but incomplete
			// files or an unstarted connector. Only exact replay completes it.
			fixture.ready = finalizations == 2
		}
		return original.Helper(ctx, target, key, payload)
	})
	if delay, err := fixture.tunnels.advance(t.Context(), tunnelServerID); err != nil || delay == 0 || finalizations != 1 {
		t.Fatalf("partial finalization did not remain pending: delay=%v finalizations=%d error=%v", delay, finalizations, err)
	}
	restarted := NewTunnels(fixture.tunnels.connection, nil, nil)
	restarted.remote = fixture.tunnels.remote
	fixture.deny = true
	if delay, err := restarted.advance(t.Context(), tunnelServerID); err != nil || delay == 0 || finalizations != 1 || fixture.posts != 1 {
		t.Fatalf("unavailable target observation terminated or replayed creation: delay=%v finalizations=%d posts=%d error=%v", delay, finalizations, fixture.posts, err)
	}
	fixture.deny = false
	if delay, err := restarted.advance(t.Context(), tunnelServerID); err != nil || delay != 0 || finalizations != 2 || fixture.posts != 1 {
		t.Fatalf("restart lost safe finalization replay: delay=%v finalizations=%d posts=%d error=%v", delay, finalizations, fixture.posts, err)
	}
	row, err := store.New(fixture.tunnels.connection.database).GetServerTunnel(t.Context(), tunnelServerID)
	if err != nil || row.Phase != "ready" || !row.CreateAttempted {
		t.Fatalf("replayed finalization lost durable result: %v", err)
	}
}

func TestTunnelCrashAfterClaimNeverSendsCreateAndAccountCannotChange(t *testing.T) {
	fixture := setupTunnel(t, false)
	now := time.Now().UnixMilli()
	if changed, err := store.New(fixture.tunnels.connection.database).ClaimTunnelCreate(t.Context(), store.ClaimTunnelCreateParams{CreateAttemptedAt: now, UpdatedAt: now, ServerID: tunnelServerID}); err != nil || changed != 1 {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := fixture.tunnels.advance(t.Context(), tunnelServerID); err != nil {
			t.Fatal(err)
		}
	}
	if fixture.posts != 0 {
		t.Fatalf("crash-before-send caused blind create: %d", fixture.posts)
	}
	if _, err := fixture.tunnels.connection.database.ExecContext(t.Context(), `UPDATE server_tunnels SET create_attempted=FALSE`); err == nil {
		t.Fatal("durable attempt reset accepted")
	}
	request := fixtureRequest()
	request.ExpectedVersion = 1
	request.AccountId = strings.Repeat("f", 32)
	if _, err := fixture.tunnels.connection.ConnectAccount(t.Context(), request); !errors.Is(err, ErrAccountInUse) {
		t.Fatalf("pending ownership account replaced: %v", err)
	}
}
func TestTunnelProviderMutationRequiresSettledNativePreparation(t *testing.T) {
	fixture := setupTunnel(t, false)
	fixture.deny = true
	if _, err := fixture.tunnels.advance(t.Context(), tunnelServerID); err != nil {
		t.Fatal(err)
	}
	row, err := store.New(fixture.tunnels.connection.database).GetServerTunnel(t.Context(), tunnelServerID)
	if err != nil || row.Phase != "needs_attention" || row.CreateAttempted || fixture.calls != 0 {
		t.Fatalf("native gate bypassed: calls=%d error=%v", fixture.calls, err)
	}
}
func TestTunnelAdmissionAtomicallyEnqueuesStateAndProtectedReplay(t *testing.T) {
	fixture := setupTunnel(t, true)
	request := &panelv1.ProvisionServerTunnelRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, ServerId: tunnelServerID, ExpectedConnectionVersion: 1}
	database := fixture.tunnels.connection.database
	if _, err := database.ExecContext(t.Context(), `CREATE TRIGGER reject_tunnel_job BEFORE UPDATE OF river_job_id ON server_tunnels BEGIN SELECT RAISE(ABORT,'fixture admission failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.tunnels.Provision(t.Context(), request); err == nil {
		t.Fatal("failed enqueue metadata reported success")
	}
	var count int
	if err := database.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM server_tunnels)+(SELECT count(*) FROM metadata_mutations)+(SELECT count(*) FROM river_job WHERE kind='cloudflare_tunnel')`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("admission partially persisted: count=%d error=%v", count, err)
	}
	if _, err := database.ExecContext(t.Context(), `DROP TRIGGER reject_tunnel_job`); err != nil {
		t.Fatal(err)
	}
	first, err := fixture.tunnels.Provision(t.Context(), request)
	if err != nil || first.Tunnel.Phase != panelv1.ServerTunnelPhase_SERVER_TUNNEL_PHASE_QUEUED {
		t.Fatalf("admission failed: %v", err)
	}
	if _, err := fixture.tunnels.connection.vault.SaveCredential(t.Context(), "cloudflare-api", 1, []byte("rotated-fixture-management-token")); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(t.Context(), `UPDATE cloudflare_integration SET version=2`); err != nil {
		t.Fatal(err)
	}
	observed, err := fixture.tunnels.Status(t.Context(), tunnelServerID)
	if err != nil || observed.Tunnel.ExpectedConnectionVersion != 1 || observed.Tunnel.RequestId != request.Mutation.RequestId {
		t.Fatalf("reload lost original replay authority after credential rotation: %v", err)
	}
	replay := &panelv1.ProvisionServerTunnelRequest{Mutation: &panelv1.Mutation{RequestId: observed.Tunnel.RequestId}, ServerId: observed.Tunnel.ServerId, ExpectedConnectionVersion: observed.Tunnel.ExpectedConnectionVersion}
	if _, err := fixture.tunnels.Provision(t.Context(), replay); err != nil {
		t.Fatalf("intent replay failed: %v", err)
	}
	if err := database.QueryRowContext(t.Context(), `SELECT count(*) FROM river_job WHERE kind='cloudflare_tunnel'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replay enqueued twice: count=%d error=%v", count, err)
	}
}

func TestTunnelExplicitResumeWaitsForTerminalRiverHandoff(t *testing.T) {
	fixture := setupTunnel(t, true)
	request := &panelv1.ProvisionServerTunnelRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, ServerId: tunnelServerID, ExpectedConnectionVersion: 1}
	if _, err := fixture.tunnels.Provision(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	database := fixture.tunnels.connection.database
	queries := store.New(database)
	original, err := queries.GetServerTunnel(t.Context(), tunnelServerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.ClaimTunnelCreate(t.Context(), store.ClaimTunnelCreateParams{CreateAttemptedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(), ServerID: tunnelServerID}); err != nil {
		t.Fatal(err)
	}
	if err := queries.AttendServerTunnel(t.Context(), store.AttendServerTunnelParams{Reason: "provider_unknown", UpdatedAt: time.Now().UnixMilli(), ServerID: tunnelServerID}); err != nil {
		t.Fatal(err)
	}
	// Simulate the small interval after our durable outcome write and before
	// River finalizes the still-running attempt. This exercises our handoff rule.
	if _, err := database.ExecContext(t.Context(), `UPDATE river_job SET state='running',attempted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, original.RiverJobID); err != nil {
		t.Fatal(err)
	}
	response, err := fixture.tunnels.Provision(t.Context(), request)
	if err != nil || response.Tunnel.Phase != panelv1.ServerTunnelPhase_SERVER_TUNNEL_PHASE_NEEDS_ATTENTION {
		t.Fatalf("active attempt was requeued: %v", err)
	}
	current, err := queries.GetServerTunnel(t.Context(), tunnelServerID)
	if err != nil || current.RiverJobID != original.RiverJobID || !current.CreateAttempted {
		t.Fatalf("active handoff changed identity/attempt: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `UPDATE river_job SET state='completed',finalized_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, original.RiverJobID); err != nil {
		t.Fatal(err)
	}
	response, err = fixture.tunnels.Provision(t.Context(), request)
	if err != nil || response.Tunnel.Phase != panelv1.ServerTunnelPhase_SERVER_TUNNEL_PHASE_CREATING {
		t.Fatalf("terminal attempt could not resume: %v", err)
	}
	current, err = queries.GetServerTunnel(t.Context(), tunnelServerID)
	if err != nil || current.RiverJobID == original.RiverJobID || !current.CreateAttempted {
		t.Fatalf("resume reused terminal job or reset mutation guard: %v", err)
	}
	fixture.row = current
	if _, err := fixture.tunnels.advance(t.Context(), tunnelServerID); err != nil || fixture.posts != 0 {
		t.Fatalf("explicit reconciliation resumed POST: posts=%d error=%v", fixture.posts, err)
	}
}

func TestTunnelWorkerRedactsPanicAfterDurableMutationClaim(t *testing.T) {
	fixture := setupTunnel(t, false)
	original := fixture.tunnels.connection.transport
	fixture.tunnels.connection.transport = fixtureTransport(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			panic("fixture-private-runtime-secret")
		}
		return original.RoundTrip(request)
	})
	worker := tunnelWorker{tunnels: fixture.tunnels}
	err := worker.Work(t.Context(), &river.Job[operations.TunnelArgs]{Args: operations.TunnelArgs{ServerID: tunnelServerID}})
	if err == nil || strings.Contains(err.Error(), "fixture-private-runtime-secret") {
		t.Fatalf("unsafe panic boundary: %v", err)
	}
	row, err := store.New(fixture.tunnels.connection.database).GetServerTunnel(t.Context(), tunnelServerID)
	if err != nil || !row.CreateAttempted {
		t.Fatalf("panic lost ambiguous mutation evidence: %v", err)
	}
}
func TestTunnelBindingPreventsManagementAuthorityChanges(t *testing.T) {
	fixture := setupTunnel(t, false)
	database := fixture.tunnels.connection.database
	for _, statement := range []string{
		`UPDATE server_management SET management_fingerprint='changed'`,
		`UPDATE server_management SET credential_version=credential_version+1`,
		`UPDATE servers SET tailscale_address='100.64.0.2'`,
		`UPDATE servers SET ssh_fingerprint='changed'`,
	} {
		if _, err := database.ExecContext(t.Context(), statement); err == nil {
			t.Fatalf("bound authority changed: %s", statement)
		}
	}
	if _, err := database.ExecContext(t.Context(), `UPDATE servers SET readiness='needs_attention'`); err != nil {
		t.Fatalf("ordinary observation update blocked: %v", err)
	}
}
