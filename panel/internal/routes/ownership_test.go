package routes

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

const ownershipAccount = "11111111111111111111111111111111"
const ownershipParentZone = "22222222222222222222222222222222"
const ownershipChildZone = "33333333333333333333333333333333"

type ownershipFixture struct {
	database                                  *sql.DB
	queries                                   *store.Queries
	server, environment, installation, tunnel string
}

func setupOwnership(t *testing.T) *ownershipFixture {
	t.Helper()
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "state"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	f := &ownershipFixture{database: database, queries: store.New(database), server: uuid.NewString(), environment: uuid.NewString(), installation: uuid.NewString(), tunnel: uuid.NewString()}
	run := func(statement string, args ...any) {
		t.Helper()
		if _, err := database.ExecContext(t.Context(), statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	run(`INSERT INTO servers(id,name,tailscale_address,ssh_fingerprint,created_at) VALUES(?,'fixture','100.64.0.1','fixture',1)`, f.server)
	run(`INSERT INTO encrypted_versions(kind,owner_id,version,ciphertext) VALUES('credential','fixture-ssh',1,?),('credential','cloudflare-api',1,?)`, []byte("fixture"), []byte("fixture"))
	run(`INSERT INTO server_management(server_id,credential_kind,credential_owner_id,credential_version,host_public_key,management_fingerprint) VALUES(?,'credential','fixture-ssh',1,'fixture','fixture')`, f.server)
	run(`INSERT INTO installation(id,installation_id,panel_server_id,bootstrap_identity,created_at) VALUES(1,?,?,?,1)`, f.installation, f.server, bytes.Repeat([]byte{1}, 32))
	run(`INSERT INTO cloudflare_integration(id,account_id,token_id,version,verified_at) VALUES(1,?,?,1,1)`, ownershipAccount, ownershipAccount)
	run(`INSERT INTO cloudflare_zones(id,name) VALUES(?,'example.com'),(?,'sub.example.com')`, ownershipParentZone, ownershipChildZone)
	run(`INSERT INTO server_tunnels(server_id,request_id,installation_id,account_id,credential_version,name,phase,create_attempted,create_attempted_at,tunnel_id,config_sha256,observed_at,created_at,updated_at) VALUES(?,?,?,?,1,'fixture','ready',TRUE,1,?,?,1,1,1)`, f.server, uuid.NewString(), f.installation, ownershipAccount, f.tunnel, strings.Repeat("a", 64))
	project := uuid.NewString()
	run(`INSERT INTO projects(id,name,repository_id,repository_owner,repository_name,workflow_id,created_at) VALUES(?,'fixture',1,'example','fixture',2,1)`, project)
	run(`INSERT INTO encrypted_versions(kind,owner_id,version,ciphertext) VALUES('environment',?,1,?)`, f.environment, []byte("fixture"))
	run(`INSERT INTO environments(id,project_id,name,server_id,branch,override_path,domains_json,auto_deploy,version,binding_version,secret_version,secret_names_json,bridge_name,created_at) VALUES(?,?,'staging',?,'main','compose.staging.yaml','[]',FALSE,1,1,1,'[]','fixture',1)`, f.environment, project, f.server)
	return f
}
func (f *ownershipFixture) operation(t *testing.T, domains []*panelv1.Domain) (string, string) {
	t.Helper()
	releaseID, operationID := uuid.NewString(), uuid.NewString()
	data, err := json.Marshal(domains)
	if err != nil {
		t.Fatal(err)
	}
	err = f.queries.CreateRelease(t.Context(), store.CreateReleaseParams{ID: releaseID, EnvironmentID: f.environment, ServerID: f.server, RepositoryID: 1, WorkflowID: 2, RunID: 3, RunAttempt: 1, Branch: "main", SourceSha: strings.Repeat("a", 40), EnvironmentVersion: 1, BindingVersion: 1, ImagesJson: []byte("[]"), DomainsJson: data, SecretVersion: 1, ComposeConfig: []byte(`{"services":{}}`), SecretBindingsJson: []byte("[]"), ConfigSha256: strings.Repeat("a", 64), CreatedAt: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app", "worker"} {
		if err := f.queries.CreateReleaseService(t.Context(), store.CreateReleaseServiceParams{ReleaseID: releaseID, Name: name, ImageDigest: "fixture"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.database.ExecContext(t.Context(), `INSERT INTO operations(id,intent_json,fingerprint,kind,server_id,environment_id,release_id,binding_version,automatic,state,outcome,created_at,updated_at) VALUES(?,'{}',?,'deploy',?,?,?,1,FALSE,'queued','pending',1,1)`, operationID, bytes.Repeat([]byte{1}, 32), f.server, f.environment, releaseID); err != nil {
		t.Fatal(err)
	}
	return operationID, releaseID
}
func (f *ownershipFixture) reserve(t *testing.T, operationID string) (Snapshot, error) {
	t.Helper()
	tx, err := f.database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	snapshot, err := ReserveOperationTx(t.Context(), tx, operationID)
	if err == nil {
		err = tx.Commit()
	}
	return snapshot, err
}
func ownershipDomains() []*panelv1.Domain {
	return []*panelv1.Domain{{Hostname: "web.sub.example.com", Service: "app", Port: 3000}}
}
func TestAdmissionSnapshotBindsCapturedReleaseAndPreservesExactReplay(t *testing.T) {
	f := setupOwnership(t)
	operationID, _ := f.operation(t, ownershipDomains())
	// Current settings do not replace the immutable accepted release inputs.
	if _, err := f.database.ExecContext(t.Context(), `UPDATE environments SET domains_json='[]'`); err != nil {
		t.Fatal(err)
	}
	first, err := f.reserve(t, operationID)
	if err != nil || len(first.Bindings) != 1 || first.Bindings[0].ZoneID != ownershipChildZone || first.Bindings[0].CredentialVersion != 1 {
		t.Fatalf("captured route reservation failed: %v", err)
	}
	if _, err := f.database.ExecContext(t.Context(), `INSERT INTO encrypted_versions(kind,owner_id,version,ciphertext) VALUES('credential','cloudflare-api',2,?)`, []byte("replacement-fixture")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.ExecContext(t.Context(), `UPDATE cloudflare_integration SET version=2`); err != nil {
		t.Fatal(err)
	}
	if err := f.queries.UpsertCloudflareZone(t.Context(), store.UpsertCloudflareZoneParams{ID: strings.Repeat("4", 32), Name: "web.sub.example.com"}); err != nil {
		t.Fatal(err)
	}
	replay, err := f.reserve(t, operationID)
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatalf("replay changed admitted provider identity/version: %v", err)
	}
	changed := ownershipDomains()
	changed[0].Service = "worker"
	changed[0].Port = 8080
	if _, err := f.database.ExecContext(t.Context(), `UPDATE operations SET state='failed',outcome='failure' WHERE id=?`, operationID); err != nil {
		t.Fatal(err)
	}
	next, _ := f.operation(t, changed)
	reused, err := f.reserve(t, next)
	if err != nil || reused.Bindings[0].ClaimID != first.Bindings[0].ClaimID || reused.Bindings[0].ZoneID != ownershipChildZone || reused.Bindings[0].CredentialVersion != 1 || reused.Bindings[0].ContainerPort != 8080 {
		t.Fatalf("same-environment claim identity changed: %v", err)
	}
	unchanged, err := OperationSnapshot(t.Context(), f.queries, operationID)
	if err != nil || !reflect.DeepEqual(first, unchanged) {
		t.Fatalf("later release changed previous route contribution: %v", err)
	}
}
func TestPermanentClaimsRejectTransferServerMovesAndSelectedZoneRemoval(t *testing.T) {
	f := setupOwnership(t)
	operationID, _ := f.operation(t, ownershipDomains())
	if _, err := f.reserve(t, operationID); err != nil {
		t.Fatal(err)
	}
	if err := CheckDesiredOwnership(t.Context(), f.queries, uuid.NewString(), f.server, ownershipDomains()); !errors.Is(err, ErrOwnership) {
		t.Fatalf("hostname transferred: %v", err)
	}
	if err := CheckDesiredOwnership(t.Context(), f.queries, f.environment, uuid.NewString(), nil); !errors.Is(err, ErrOwnership) {
		t.Fatalf("domain removal allowed server rebind: %v", err)
	}
	if err := f.queries.DeleteCloudflareZone(t.Context(), ownershipChildZone); err == nil {
		t.Fatal("claimed zone removal accepted")
	}
	if _, err := f.database.ExecContext(t.Context(), `DELETE FROM route_claims`); err == nil {
		t.Fatal("permanent hostname ownership released")
	}
}
func TestOldCapturedReleaseCannotStealNewDesiredOwnerAndSuffixesMatchLabels(t *testing.T) {
	for _, name := range []string{"new desired owner", "unselected zone"} {
		t.Run(name, func(t *testing.T) {
			f := setupOwnership(t)
			domains := ownershipDomains()
			if name == "unselected zone" {
				domains[0].Hostname = "attackersubexample.com"
			}
			operationID, _ := f.operation(t, domains)
			want := ErrSetup
			if name == "new desired owner" {
				// An older captured release must not acquire a name now selected elsewhere.
				other := uuid.NewString()
				if _, err := f.database.ExecContext(t.Context(), `INSERT INTO encrypted_versions(kind,owner_id,version,ciphertext) VALUES('environment',?,1,?)`, other, []byte("fixture")); err != nil {
					t.Fatal(err)
				}
				if _, err := f.database.ExecContext(t.Context(), `INSERT INTO environments(id,project_id,name,server_id,branch,override_path,domains_json,auto_deploy,version,binding_version,secret_version,secret_names_json,bridge_name,created_at) SELECT ?,project_id,'production',server_id,branch,override_path,'[]',FALSE,1,1,1,'[]','other',1 FROM environments LIMIT 1`, other); err != nil {
					t.Fatal(err)
				}
				if err := f.queries.ReserveDesiredDomain(t.Context(), store.ReserveDesiredDomainParams{Hostname: domains[0].Hostname, EnvironmentID: other}); err != nil {
					t.Fatal(err)
				}
				want = ErrOwnership
			}
			if _, err := f.reserve(t, operationID); !errors.Is(err, want) {
				t.Fatalf("invalid captured ownership accepted: %v", err)
			}
			count, err := f.queries.RouteClaimCount(t.Context())
			if err != nil || count != 0 {
				t.Fatalf("rejected admission retained claims: %d %v", count, err)
			}
		})
	}
}
func TestSnapshotRejectsUnreservedAndMismatchedOperationIdentity(t *testing.T) {
	f := setupOwnership(t)
	operationID, _ := f.operation(t, ownershipDomains())
	if _, err := OperationSnapshot(t.Context(), f.queries, operationID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing reservation became an authorized snapshot: %v", err)
	}
	if _, err := f.reserve(t, operationID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.ExecContext(t.Context(), `UPDATE operations SET server_id=? WHERE id=?`, uuid.NewString(), operationID); err != nil {
		t.Fatal(err)
	}
	if _, err := OperationSnapshot(t.Context(), f.queries, operationID); !errors.Is(err, ErrOwnership) {
		t.Fatalf("operation assignment mismatch accepted: %v", err)
	}
}
func TestEmptyRouteReleaseNeedsNoProviderBinding(t *testing.T) {
	f := setupOwnership(t)
	operationID, _ := f.operation(t, nil)
	if _, err := f.database.ExecContext(t.Context(), `DELETE FROM server_tunnels`); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.reserve(t, operationID)
	if err != nil || len(snapshot.Bindings) != 0 || snapshot.AccountID != "" || len(snapshot.SHA256) != 64 {
		t.Fatalf("private-only release required provider setup: %v", err)
	}
}

func TestRollbackSnapshotPreservesTargetReleaseAcrossBranchBindingChanges(t *testing.T) {
	f := setupOwnership(t)
	operationID, releaseID := f.operation(t, ownershipDomains())
	original, err := f.reserve(t, operationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.ExecContext(t.Context(), `UPDATE operations SET state='failed',outcome='failure' WHERE id=?`, operationID); err != nil {
		t.Fatal(err)
	}
	rollbackID := uuid.NewString()
	if _, err := f.database.ExecContext(t.Context(), `INSERT INTO operations(id,intent_json,fingerprint,kind,server_id,environment_id,release_id,binding_version,automatic,state,outcome,created_at,updated_at) VALUES(?,'{}',?,'rollback',?,?,?,2,FALSE,'queued','pending',1,1)`, rollbackID, bytes.Repeat([]byte{1}, 32), f.server, f.environment, releaseID); err != nil {
		t.Fatal(err)
	}
	rollback, err := f.reserve(t, rollbackID)
	if err != nil || rollback.ReleaseID != original.ReleaseID || !reflect.DeepEqual(rollback.Bindings, original.Bindings) || rollback.OperationID == original.OperationID {
		t.Fatalf("rollback replaced retained route snapshot with current binding: %v", err)
	}
}
