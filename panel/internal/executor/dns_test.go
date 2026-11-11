package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/cloudflareflow"
	"github.com/aleksdotbar/vps/panel/internal/deployment"
	"github.com/aleksdotbar/vps/panel/internal/hosthelper"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/releases"
	"github.com/aleksdotbar/vps/panel/internal/remote"
	"github.com/aleksdotbar/vps/panel/internal/routes"
	"github.com/aleksdotbar/vps/panel/internal/store"
	"github.com/google/uuid"
)

type dnsTransport func(*http.Request) (*http.Response, error)

func (f dnsTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDNSConfirmationRequiresNativeCandidateAndProgressesAcrossBoundedBatches(t *testing.T) {
	f := setupNativeInput(t)
	ctx := t.Context()
	queries := store.New(f.database)
	installation, err := queries.GetInstallation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.vault.SaveCredential(ctx, "cloudflare-api", 0, []byte("fixture-token")); err != nil {
		t.Fatal(err)
	}
	run := func(statement string, args ...any) {
		t.Helper()
		if _, err := f.database.ExecContext(ctx, statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	account, zone := strings.Repeat("a", 32), strings.Repeat("b", 32)
	run(`INSERT INTO cloudflare_integration(id,account_id,token_id,version,verified_at) VALUES(1,?,?,1,1)`, account, strings.Repeat("c", 32))
	run(`INSERT INTO cloudflare_zones(id,name) VALUES(?,'example.com')`, zone)
	run(`INSERT INTO server_tunnels(server_id,request_id,installation_id,account_id,credential_version,name,phase,create_attempted,create_attempted_at,tunnel_id,config_sha256,observed_at,created_at,updated_at) VALUES(?,?,?,?,1,'fixture','ready',TRUE,1,?,?,1,1,1)`, f.operation.ServerID, uuid.NewString(), installation.InstallationID, account, uuid.NewString(), strings.Repeat("a", 64))
	domains := make([]*panelv1.Domain, 5)
	for i := range domains {
		domains[i] = &panelv1.Domain{Hostname: fmt.Sprintf("app-%d.example.com", i), Service: "app", Port: 3000}
	}
	domainData, err := json.Marshal(domains)
	if err != nil {
		t.Fatal(err)
	}
	release, err := queries.GetRelease(ctx, f.candidate)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := releases.SnapshotSHA256(release.ComposeConfig, domainData, f.operation.EnvironmentID, 2)
	if err != nil {
		t.Fatal(err)
	}
	run(`UPDATE releases SET domains_json=?,config_sha256=? WHERE id=?`, domainData, hash, f.candidate)
	tx, err := f.database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	snapshot, err := routes.ReserveOperationTx(ctx, tx, f.operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(ctx, f.database, f.operation); err != nil {
		t.Fatal(err)
	}

	records := map[string]map[string]any{}
	posts := 0
	transport := dnsTransport(func(request *http.Request) (*http.Response, error) {
		var result any
		switch request.Method {
		case http.MethodPost:
			record := map[string]any{}
			if err := json.NewDecoder(request.Body).Decode(&record); err != nil {
				return nil, err
			}
			posts++
			record["id"] = fmt.Sprintf("%032x", posts)
			record["proxiable"] = true
			record["created_on"] = time.Now().UTC().Format(time.RFC3339)
			records[record["name"].(string)] = record
			result = record
		case http.MethodGet:
			if strings.HasSuffix(request.URL.Path, "/dns_records") {
				result = []any{}
				if record := records[request.URL.Query().Get("name.exact")]; record != nil {
					result = []any{record}
				}
			} else {
				for _, record := range records {
					if strings.HasSuffix(request.URL.Path, "/"+record["id"].(string)) {
						result = record
					}
				}
			}
		default:
			t.Fatalf("unexpected provider mutation: %s", request.Method)
		}
		data, err := json.Marshal(map[string]any{"success": true, "result": result})
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(data)), Request: request}, nil
	})
	service := New(f.database, f.cipher, nil, cloudflareflow.New(f.database, f.cipher, transport))
	finalSystem := "/nix/store/" + strings.Repeat("a", 32) + "-nixos-system-fixture"
	phase := "checking"
	confirmations := 0
	service.remote = fixtureHelper(func(_ context.Context, _ remote.Target, _ []byte, payload []byte) (remote.Result, error) {
		var response any
		if request, err := hosthelper.ParseReadinessRequest(payload); err == nil {
			response = hosthelper.ReadinessResponse{SchemaVersion: 1, Action: "readiness", InstallationID: request.InstallationID, ServerID: request.ServerID, Challenge: request.Challenge, Role: "panel", Stage: "final", CurrentSystem: finalSystem, SystemProfile: finalSystem, BootedSystem: finalSystem, Status: "ready", DockerVersion: hosthelper.DockerVersion, ComposeVersion: hosthelper.ComposeVersion}
		} else {
			request, err := deployment.ParseExecution(payload)
			if err != nil {
				t.Fatal(err)
			}
			if request.Action == "deploy_confirm" {
				confirmations++
				if len(records) != 5 {
					t.Fatal("confirmed before all DNS records verified")
				}
				phase = "succeeded"
			} else if request.Action != "deploy_observe" {
				t.Fatalf("unexpected effect: %s", request.Action)
			}
			response = deployment.ExecutionResponse{SchemaVersion: 1, Action: request.Action, Challenge: request.Challenge, Identity: request.Identity, ManifestSHA256: request.ManifestSHA256, FinalSystem: finalSystem, Phase: phase, ActiveReleaseID: f.candidate, RouteRevision: 1, RouteConfigSHA256: strings.Repeat("d", 64), Services: []deployment.ServiceObservation{{Service: "app", Image: f.image, ContainerID: strings.Repeat("e", 64), State: "running", Health: "healthy"}}}
		}
		data, err := json.Marshal(response)
		return remote.Result{Outcome: remote.CommandExited, Output: data}, err
	})
	if err := service.AuthorizeDNS(ctx, f.operation, snapshot, snapshot.Bindings[0].Hostname); err == nil {
		t.Fatal("candidate not awaiting DNS authorized mutation")
	}
	phase = "awaiting_dns"
	if err := service.AuthorizeDNS(ctx, f.operation, snapshot, "unowned.example.com"); err == nil {
		t.Fatal("unowned hostname authorized")
	}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := service.Reconcile(ctx, f.operation)
		expected := operations.Running
		if attempt == 1 {
			expected = operations.Succeeded
		}
		if err != nil || result != expected {
			t.Fatalf("batch %d: result=%v error=%v", attempt, result, err)
		}
		if attempt == 0 && (posts != 4 || confirmations != 0) {
			t.Fatalf("unbounded batch or early confirmation: posts=%d confirmations=%d", posts, confirmations)
		}
	}
	if posts != 5 || confirmations != 1 {
		t.Fatalf("repeated DNS create or missing confirmation: posts=%d confirmations=%d", posts, confirmations)
	}
}
