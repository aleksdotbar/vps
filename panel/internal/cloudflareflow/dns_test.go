package cloudflareflow

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/routes"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

type dnsAuthorizerFunc func(context.Context, store.Operation, routes.Snapshot, string) error

func (f dnsAuthorizerFunc) AuthorizeDNS(ctx context.Context, operation store.Operation, snapshot routes.Snapshot, hostname string) error {
	return f(ctx, operation, snapshot, hostname)
}

type dnsFixture struct {
	connection *Service
	records    *DNSRecords
	request    DNSRequest
	snapshot   routes.Snapshot
	simulator  *dnsSimulator
}

func setupDNS(t *testing.T) *dnsFixture {
	t.Helper()
	return setupDNSDeadline(t, time.Now().Add(time.Minute))
}

func setupDNSDeadline(t *testing.T, deadline time.Time) *dnsFixture {
	t.Helper()
	connection := fixtureService(t)
	if _, err := connection.vault.SaveCredential(t.Context(), "cloudflare-api", 0, []byte("dns-fixture-one")); err != nil {
		t.Fatal(err)
	}
	server, environment, installation, tunnel, project, release, operation := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	run := func(statement string, args ...any) {
		t.Helper()
		if _, err := connection.database.ExecContext(t.Context(), statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	run(`INSERT INTO servers(id,name,tailscale_address,ssh_fingerprint,created_at) VALUES(?,'fixture','100.64.0.1','fixture',1)`, server)
	run(`INSERT INTO encrypted_versions(kind,owner_id,version,ciphertext) VALUES('credential','fixture-ssh',1,?),('environment',?,1,?)`, []byte("fixture"), environment, []byte("fixture"))
	run(`INSERT INTO server_management(server_id,credential_kind,credential_owner_id,credential_version,host_public_key,management_fingerprint) VALUES(?,'credential','fixture-ssh',1,'fixture','fixture')`, server)
	run(`INSERT INTO installation(id,installation_id,panel_server_id,operation_id,input_sha256,source_system,enrollment_system,bootstrap_identity,created_at) VALUES(1,?,?,?,'fixture','fixture','fixture',?,1)`, installation, server, uuid.NewString(), bytes.Repeat([]byte{1}, 32))
	run(`INSERT INTO cloudflare_integration(id,account_id,token_id,version,verified_at) VALUES(1,?,?,1,1)`, fixtureAccount, fixtureTokenID)
	run(`INSERT INTO cloudflare_zones(id,name) VALUES(?,'example.com')`, fixtureZone)
	run(`INSERT INTO server_tunnels(server_id,request_id,installation_id,account_id,credential_version,name,phase,create_attempted,create_attempted_at,tunnel_id,config_sha256,observed_at,created_at,updated_at) VALUES(?,?,?,?,1,'fixture','ready',TRUE,1,?,?,1,1,1)`, server, uuid.NewString(), installation, fixtureAccount, tunnel, strings.Repeat("a", 64))
	run(`INSERT INTO projects(id,name,repository_id,repository_owner,repository_name,workflow_id,created_at) VALUES(?,'fixture',1,'example','fixture',2,1)`, project)
	run(`INSERT INTO environments(id,project_id,name,server_id,branch,override_path,domains_json,auto_deploy,version,binding_version,secret_version,secret_names_json,bridge_name,created_at) VALUES(?,?,'staging',?,'main','compose.staging.yaml','[]',FALSE,1,1,1,'[]','fixture',1)`, environment, project, server)
	domains, err := json.Marshal([]*panelv1.Domain{{Hostname: "web.example.com", Service: "app", Port: 3000}})
	if err != nil {
		t.Fatal(err)
	}
	queries := store.New(connection.database)
	err = queries.CreateRelease(t.Context(), store.CreateReleaseParams{ID: release, EnvironmentID: environment, ServerID: server, RepositoryID: 1, WorkflowID: 2, RunID: 3, RunAttempt: 1, Branch: "main", SourceSha: strings.Repeat("a", 40), EnvironmentVersion: 1, BindingVersion: 1, ImagesJson: []byte("[]"), DomainsJson: domains, SecretVersion: 1, ComposeConfig: []byte(`{"services":{}}`), SecretBindingsJson: []byte("[]"), ConfigSha256: strings.Repeat("a", 64), CreatedAt: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	if err := queries.CreateReleaseService(t.Context(), store.CreateReleaseServiceParams{ReleaseID: release, Name: "app", ImageDigest: "fixture"}); err != nil {
		t.Fatal(err)
	}
	run(`INSERT INTO operations(id,intent_json,fingerprint,kind,server_id,environment_id,release_id,binding_version,automatic,state,outcome,started,fence,execution_deadline,created_at,updated_at) VALUES(?,'{}',?,'deploy',?,?,?,1,FALSE,'running','unknown',TRUE,1,?,1,1)`, operation, bytes.Repeat([]byte{1}, 32), server, environment, release, deadline.UnixMilli())
	tx, err := connection.database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	snapshot, err := routes.ReserveOperationTx(t.Context(), tx, operation)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f := &dnsFixture{connection: connection, snapshot: snapshot, request: DNSRequest{OperationID: operation, Fence: 1, RouteSnapshotSHA256: snapshot.SHA256, Hostname: snapshot.Bindings[0].Hostname}}
	f.simulator = &dnsSimulator{t: t, database: connection.database, snapshot: snapshot}
	connection.transport = f.simulator
	f.records = NewDNSRecords(connection, dnsAuthorizerFunc(func(ctx context.Context, stored store.Operation, captured routes.Snapshot, hostname string) error {
		if stored.ID != operation || stored.Fence != 1 || captured.SHA256 != snapshot.SHA256 || hostname != f.request.Hostname {
			return ErrDNSPrecondition
		}
		// This substitutes the future native prerequisite boundary only. It is
		// not evidence that an executor or SSH authorizer exists or succeeds.
		return nil
	}))
	return f
}

type dnsSimulator struct {
	t            *testing.T
	database     *sql.DB
	snapshot     routes.Snapshot
	mu           sync.Mutex
	records      []map[string]any
	posts, reads int
	lostResponse bool
	beforeList   func()
	tokens       []string
}

func (s *dnsSimulator) record() map[string]any {
	return map[string]any{"id": strings.Repeat("a", 32), "type": "CNAME", "name": s.snapshot.Bindings[0].Hostname, "content": s.snapshot.TunnelID + ".cfargotunnel.com", "proxied": true, "proxiable": true, "ttl": 1, "comment": dnsComment(s.snapshot, s.snapshot.Bindings[0]), "created_on": time.Now().UTC().Format(time.RFC3339), "modified_on": time.Now().UTC().Format(time.RFC3339), "meta": map[string]any{}}
}

func (s *dnsSimulator) RoundTrip(request *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if request.URL.Host != "api.cloudflare.com" || request.URL.Scheme != "https" {
		s.t.Error("provider origin escaped")
		return nil, ErrProvider
	}
	s.tokens = append(s.tokens, request.Header.Get("Authorization"))
	var result any
	switch request.Method {
	case http.MethodPost:
		s.posts++
		// A real SQL read at the I/O boundary proves the transaction is released
		// and the irreversible claim already exists before provider mutation.
		attempt, err := store.New(s.database).GetDNSRecord(request.Context(), s.snapshot.Bindings[0].ClaimID)
		if err != nil || attempt.CreateAttemptedAt < 1 {
			s.t.Error("POST preceded durable attempt or held SQLite")
			return nil, ErrProvider
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			s.t.Error(err)
			return nil, ErrProvider
		}
		expected := s.record()
		for _, key := range []string{"type", "name", "content", "proxied", "ttl", "comment"} {
			got, _ := json.Marshal(body[key])
			want, _ := json.Marshal(expected[key])
			if !bytes.Equal(got, want) {
				s.t.Errorf("unexpected POST %s: %s", key, got)
				return nil, ErrProvider
			}
		}
		if len(body) != 6 || len(body["comment"].(string)) > 100 {
			s.t.Error("unexpected create fields")
			return nil, ErrProvider
		}
		s.records = []map[string]any{expected}
		if s.lostResponse {
			return nil, errors.New("simulated credential-bearing lost response")
		}
		result = expected
	case http.MethodGet:
		s.reads++
		if strings.HasSuffix(request.URL.Path, "/dns_records") {
			if s.beforeList != nil {
				s.beforeList()
			}
			if request.URL.Query().Get("name.exact") != s.snapshot.Bindings[0].Hostname || request.URL.Query().Get("per_page") != "2" || request.URL.Query().Get("page") != "1" || len(request.URL.Query()) != 3 {
				s.t.Error("unbounded/filtered ownership read")
				return nil, ErrProvider
			}
			result = s.records
			if s.records == nil {
				result = []any{}
			}
		} else {
			if len(s.records) == 0 {
				return nil, errors.New("owned record disappeared")
			}
			result = s.records[0]
		}
	default:
		s.t.Error("non-create mutation escaped")
		return nil, ErrProvider
	}
	encoded, err := json.Marshal(map[string]any{"success": true, "result": result})
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(encoded)), Request: request}, nil
}

func TestDNSCreateClaimsBeforePOSTAndRechecksExactRecord(t *testing.T) {
	f := setupDNS(t)
	observation, err := f.records.Ensure(t.Context(), f.request)
	if err != nil || observation.RecordID != strings.Repeat("a", 32) || observation.ObservedAt.IsZero() || f.simulator.posts != 1 || f.simulator.reads != 3 {
		t.Fatalf("create: observation=%v error=%v POSTs=%d reads=%d", observation, err, f.simulator.posts, f.simulator.reads)
	}
	if _, err := f.records.Ensure(t.Context(), f.request); err != nil || f.simulator.posts != 1 {
		t.Fatalf("replay mutated DNS: %v", err)
	}
	for _, statement := range []string{`DELETE FROM dns_records`, `UPDATE dns_records SET create_attempted_at=1`, `UPDATE dns_records SET record_id='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'`} {
		if _, err := f.connection.database.ExecContext(t.Context(), statement); err == nil {
			t.Errorf("immutable attempt accepted %s", statement)
		}
	}
	f.simulator.records[0]["id"] = strings.Repeat("b", 32)
	if _, err := f.records.Ensure(t.Context(), f.request); !errors.Is(err, ErrDNSConflict) || f.simulator.posts != 1 {
		t.Fatalf("record identity replacement accepted: %v", err)
	}
}

func TestDNSLostCreateResponseReconcilesWithoutNewPOSTAndUsesCurrentToken(t *testing.T) {
	f := setupDNS(t)
	// Admission retained version 1; the current same-account token authorizes
	// this first attempt independently of that immutable route snapshot.
	if _, err := f.connection.vault.SaveCredential(t.Context(), "cloudflare-api", 1, []byte("dns-fixture-two")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.connection.database.ExecContext(t.Context(), `UPDATE cloudflare_integration SET version=2`); err != nil {
		t.Fatal(err)
	}
	f.simulator.lostResponse = true
	if _, err := f.records.Ensure(t.Context(), f.request); !errors.Is(err, ErrDNSUnknown) {
		t.Fatalf("lost create: %v", err)
	}
	row, err := store.New(f.connection.database).GetDNSRecord(t.Context(), f.snapshot.Bindings[0].ClaimID)
	if err != nil || row.RecordID.Valid || row.CredentialVersion != 2 || f.snapshot.Bindings[0].CredentialVersion != 1 || f.simulator.tokens[0] != "Bearer dns-fixture-two" {
		t.Fatalf("lost response evidence: %v %v", row, err)
	}
	if _, err := f.connection.vault.SaveCredential(t.Context(), "cloudflare-api", 2, []byte("dns-fixture-three")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.connection.database.ExecContext(t.Context(), `UPDATE cloudflare_integration SET version=3`); err != nil {
		t.Fatal(err)
	}
	observation, err := f.records.Ensure(t.Context(), f.request)
	if err != nil || observation.RecordID == "" || f.simulator.posts != 1 || f.simulator.tokens[len(f.simulator.tokens)-1] != "Bearer dns-fixture-three" {
		t.Fatalf("readonly reconcile/rotation: %v", err)
	}
	after, err := store.New(f.connection.database).GetDNSRecord(t.Context(), row.ClaimID)
	if err != nil || after.OperationID != row.OperationID || after.OperationFence != row.OperationFence || after.RouteSnapshotSha256 != row.RouteSnapshotSha256 || after.CredentialVersion != row.CredentialVersion || after.CreateAttemptedAt != row.CreateAttemptedAt {
		t.Fatalf("reconciliation changed original create evidence: %v", err)
	}
}

func TestDNSLaterOperationReconcilesOriginalUncertainCreateEvidence(t *testing.T) {
	f := setupDNS(t)
	f.simulator.lostResponse = true
	if _, err := f.records.Ensure(t.Context(), f.request); !errors.Is(err, ErrDNSUnknown) {
		t.Fatal(err)
	}
	row, err := store.New(f.connection.database).GetDNSRecord(t.Context(), f.snapshot.Bindings[0].ClaimID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.connection.database.ExecContext(t.Context(), `UPDATE operations SET state='failed',outcome='failure' WHERE id=?`, f.request.OperationID); err != nil {
		t.Fatal(err)
	}
	next := uuid.NewString()
	if _, err := f.connection.database.ExecContext(t.Context(), `INSERT INTO operations(id,intent_json,fingerprint,kind,server_id,environment_id,release_id,binding_version,automatic,state,outcome,started,fence,execution_deadline,created_at,updated_at) VALUES(?,'{}',?,'rollback',?,?,?,2,FALSE,'recovering','unknown',TRUE,4,?,1,1)`, next, bytes.Repeat([]byte{1}, 32), f.snapshot.ServerID, f.snapshot.EnvironmentID, f.snapshot.ReleaseID, time.Now().Add(time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := routes.OperationSnapshot(t.Context(), store.New(f.connection.database), next)
	if err != nil {
		t.Fatal(err)
	}
	f.records.authorizer = dnsAuthorizerFunc(func(context.Context, store.Operation, routes.Snapshot, string) error { return nil })
	request := DNSRequest{OperationID: next, Fence: 4, RouteSnapshotSHA256: snapshot.SHA256, Hostname: f.request.Hostname}
	if _, err := f.records.Ensure(t.Context(), request); err != nil || f.simulator.posts != 1 {
		t.Fatalf("later operation repeated create: %v", err)
	}
	after, err := store.New(f.connection.database).GetDNSRecord(t.Context(), row.ClaimID)
	if err != nil || after.OperationID != row.OperationID || after.OperationFence != row.OperationFence || after.RouteSnapshotSha256 != row.RouteSnapshotSha256 {
		t.Fatalf("original create evidence changed: %v", err)
	}
}

func TestDNSRedactsNativeBoundaryPanic(t *testing.T) {
	f := setupDNS(t)
	f.records.authorizer = dnsAuthorizerFunc(func(context.Context, store.Operation, routes.Snapshot, string) error {
		panic("credential-bearing native panic")
	})
	if _, err := f.records.Ensure(t.Context(), f.request); !errors.Is(err, ErrProvider) || strings.Contains(err.Error(), "credential-bearing") || f.simulator.posts != 0 || f.simulator.reads != 0 {
		t.Fatalf("native panic escaped: %v", err)
	}
}

func TestDNSUncertainAbsentRecordNeverRetriesPOST(t *testing.T) {
	f := setupDNS(t)
	f.simulator.lostResponse = true
	if _, err := f.records.Ensure(t.Context(), f.request); !errors.Is(err, ErrDNSUnknown) {
		t.Fatal(err)
	}
	f.simulator.records = nil
	for range 2 {
		if _, err := f.records.Ensure(t.Context(), f.request); !errors.Is(err, ErrDNSUnknown) || f.simulator.posts != 1 {
			t.Fatalf("uncertain create retried: %v POSTs=%d", err, f.simulator.posts)
		}
	}
}

func TestDNSRejectsForeignPreexistingAndMismatchedReconciliation(t *testing.T) {
	for _, scenario := range []string{"identical preexisting", "multiple", "comment", "content", "type", "proxied", "ttl", "created_on"} {
		t.Run(scenario, func(t *testing.T) {
			f := setupDNS(t)
			if scenario == "identical preexisting" {
				f.simulator.records = []map[string]any{f.simulator.record()}
			} else {
				f.simulator.lostResponse = true
				if _, err := f.records.Ensure(t.Context(), f.request); !errors.Is(err, ErrDNSUnknown) {
					t.Fatal(err)
				}
				switch scenario {
				case "multiple":
					f.simulator.records = append(f.simulator.records, f.simulator.record())
				case "comment":
					f.simulator.records[0]["comment"] = "foreign"
				case "content":
					f.simulator.records[0]["content"] = "foreign.cfargotunnel.com"
				case "type":
					f.simulator.records[0]["type"] = "TXT"
				case "proxied":
					f.simulator.records[0]["proxied"] = false
				case "ttl":
					f.simulator.records[0]["ttl"] = 300
				case "created_on":
					f.simulator.records[0]["created_on"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
				}
			}
			prior := f.simulator.posts
			if _, err := f.records.Ensure(t.Context(), f.request); !errors.Is(err, ErrDNSConflict) || f.simulator.posts != prior {
				t.Fatalf("foreign record accepted/mutated: %v", err)
			}
			if scenario == "identical preexisting" {
				if _, err := store.New(f.connection.database).GetDNSRecord(t.Context(), f.snapshot.Bindings[0].ClaimID); !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("unmanaged preexisting adopted: %v", err)
				}
			}
		})
	}
}

func TestDNSRequiresNativeGateAndRechecksFenceAfterProviderRead(t *testing.T) {
	f := setupDNS(t)
	withoutGate := NewDNSRecords(f.connection, nil)
	if _, err := withoutGate.Ensure(t.Context(), f.request); !errors.Is(err, ErrDNSPrecondition) || f.simulator.reads != 0 {
		t.Fatalf("missing native gate escaped: %v", err)
	}
	f.simulator.beforeList = func() {
		if _, err := f.connection.database.ExecContext(t.Context(), `UPDATE operations SET fence=fence+1 WHERE id=?`, f.request.OperationID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.records.Ensure(t.Context(), f.request); !errors.Is(err, ErrDNSPrecondition) || f.simulator.posts != 0 {
		t.Fatalf("stale fence mutated provider: %v", err)
	}
	if _, err := store.New(f.connection.database).GetDNSRecord(t.Context(), f.snapshot.Bindings[0].ClaimID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale fence claimed POST: %v", err)
	}
}

func TestDNSRejectsExpiredInactiveAndChangedSnapshotBeforeProviderIO(t *testing.T) {
	for _, scenario := range []string{"expired", "queued", "snapshot", "hostname", "native denial"} {
		t.Run(scenario, func(t *testing.T) {
			deadline := time.Now().Add(time.Minute)
			if scenario == "expired" {
				deadline = time.Now().Add(-time.Second)
			}
			f := setupDNSDeadline(t, deadline)
			switch scenario {
			case "expired":
			case "queued":
				if _, err := f.connection.database.ExecContext(t.Context(), `UPDATE operations SET state='queued'`); err != nil {
					t.Fatal(err)
				}
			case "snapshot":
				f.request.RouteSnapshotSHA256 = strings.Repeat("b", 64)
			case "hostname":
				f.request.Hostname = "foreign.example.com"
			case "native denial":
				f.records.authorizer = dnsAuthorizerFunc(func(context.Context, store.Operation, routes.Snapshot, string) error {
					return errors.New("sensitive native denial")
				})
			}
			if _, err := f.records.Ensure(t.Context(), f.request); !errors.Is(err, ErrDNSPrecondition) || f.simulator.posts != 0 || f.simulator.reads != 0 {
				t.Fatalf("precondition escaped: %v", err)
			}
		})
	}
}

func TestDNSConcurrentCallersCannotClaimSecondPOST(t *testing.T) {
	f := setupDNS(t)
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	f.records.authorizer = dnsAuthorizerFunc(func(ctx context.Context, _ store.Operation, _ routes.Snapshot, _ string) error {
		ready <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	results := make(chan error, 2)
	for range 2 {
		go func() { _, err := f.records.Ensure(t.Context(), f.request); results <- err }()
	}
	for range 2 {
		select {
		case <-ready:
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent admission retained SQLite or exceeded permit bound")
		}
	}
	close(release)
	for range 2 {
		if err := <-results; err != nil && !errors.Is(err, ErrDNSConflict) && !errors.Is(err, ErrDNSUnknown) {
			t.Fatal(err)
		}
	}
	if f.simulator.posts != 1 {
		t.Fatalf("concurrent POST count=%d", f.simulator.posts)
	}
}
