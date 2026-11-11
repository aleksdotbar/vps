package projects

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/compose"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/routes"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

func releaseSetup(t *testing.T) (*Service, *sql.DB, *operations.Runtime, *compose.Resolver, ReleaseInput) {
	t.Helper()
	ctx := context.Background()
	service, database, serverID := setup(t)
	seedReleaseRouting(t, service, serverID)
	if err := store.New(database).ObserveServer(ctx, store.ObserveServerParams{Readiness: "ready", ObservedAt: time.Now().UnixMilli(), ID: serverID}); err != nil {
		t.Fatal(err)
	}
	_, environment := projectAndEnvironment(t, service, serverID)
	saved, err := service.SaveSecrets(ctx, &panelv1.SaveEnvironmentSecretsRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, EnvironmentId: environment.Id, ExpectedVersion: 1, Upserts: []*panelv1.SecretValue{{Name: "API_TOKEN", Value: "captured-private-api"}, {Name: "WORKER_TOKEN", Value: "captured-private-worker"}}})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := operations.New(ctx, database, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	version, err := storeVersion(saved.Environment.Version)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := service.CaptureSelection(ctx, tx, environment.Id, version)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	source, err := os.OpenRoot("../../../examples/stateless")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	destination, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = destination.Close() }()
	for _, name := range []string{"compose.yaml", "compose.staging.yaml"} {
		data, err := source.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := destination.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	artifact, err := os.ReadFile("../../../examples/stateless/release.json")
	if err != nil {
		t.Fatal(err)
	}
	command := os.Getenv("PANEL_TEST_COMPOSE")
	if command == "" {
		command = "docker-compose"
	}
	resolver, err := compose.New(command)
	if err != nil {
		t.Fatal(err)
	}
	intent := operations.Intent{RequestID: uuid.NewString(), Kind: operations.Deploy, EnvironmentID: environment.Id, ExpectedVersion: selection.Environment.Version, SourceSHA: "0123456789abcdef0123456789abcdef01234567"}
	input := ReleaseInput{Intent: intent, Selection: selection, Build: Build{RepositoryID: 1, WorkflowID: 2, RunID: 3, RunAttempt: 1, Branch: "main", SourceSHA: intent.SourceSHA}, Tree: directory, Artifact: artifact}
	return service, database, runtime, resolver, input
}
func seedReleaseRouting(t *testing.T, service *Service, serverID string) {
	t.Helper()
	ctx := t.Context()
	account := strings.Repeat("1", 32)
	zone := strings.Repeat("2", 32)
	installationID := uuid.NewString()
	if _, err := service.vault.SaveCredential(ctx, "cloudflare-api", 0, []byte("fixture-cloudflare")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.vault.SaveCredential(ctx, "fixture-ssh", 0, []byte("fixture-ssh")); err != nil {
		t.Fatal(err)
	}
	queries := store.New(service.database)
	if err := queries.SetCloudflareIntegration(ctx, store.SetCloudflareIntegrationParams{AccountID: account, TokenID: account, Version: 1, VerifiedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := queries.UpsertCloudflareZone(ctx, store.UpsertCloudflareZoneParams{ID: zone, Name: "example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := queries.CreateServerManagement(ctx, store.CreateServerManagementParams{ServerID: serverID, CredentialKind: "credential", CredentialOwnerID: "fixture-ssh", CredentialVersion: 1, HostPublicKey: "fixture", ManagementFingerprint: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := queries.CreateInstallation(ctx, store.CreateInstallationParams{InstallationID: installationID, PanelServerID: serverID, OperationID: uuid.NewString(), InputSha256: "fixture", SourceSystem: "fixture", EnrollmentSystem: "fixture", BootstrapIdentity: bytes.Repeat([]byte{1}, 32), CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.database.ExecContext(ctx, `INSERT INTO server_tunnels(server_id,request_id,installation_id,account_id,credential_version,name,phase,create_attempted,create_attempted_at,tunnel_id,config_sha256,observed_at,created_at,updated_at) VALUES(?,?,?,?,1,'fixture','ready',TRUE,1,?,?,1,1,1)`, serverID, uuid.NewString(), installationID, account, uuid.NewString(), strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
}
func TestQueuedReleaseKeepsCapturedDomainsAndSecretsAndReplaysBeforeSourceIO(t *testing.T) {
	service, database, runtime, resolver, input := releaseSetup(t)
	ctx := context.Background()
	saved, err := service.SaveSecrets(ctx, &panelv1.SaveEnvironmentSecretsRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, EnvironmentId: input.Selection.Environment.ID, ExpectedVersion: 2, Upserts: []*panelv1.SecretValue{{Name: "API_TOKEN", Value: "new-pending-private-api"}}})
	if err != nil {
		t.Fatal(err)
	}
	settings := proto.Clone(saved.Environment.Settings).(*panelv1.EnvironmentSettings)
	settings.Domains[0].Hostname = "pending.example.com"
	if _, err := service.UpdateEnvironment(ctx, &panelv1.UpdateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Id: saved.Environment.Id, ExpectedVersion: saved.Environment.Version, Settings: settings}); err != nil {
		t.Fatal(err)
	}
	release, operation, err := service.QueueRelease(ctx, runtime, resolver, input)
	if err != nil {
		t.Fatal(err)
	}
	if release.SecretVersion != 2 || !bytes.Contains(release.DomainsJson, []byte("app.example.com")) || bytes.Contains(release.DomainsJson, []byte("pending.example.com")) {
		t.Fatal("pending edits changed captured release")
	}
	for _, secret := range []string{"captured-private-api", "captured-private-worker", "new-pending-private-api"} {
		if bytes.Contains(release.ComposeConfig, []byte(secret)) || bytes.Contains(release.SecretBindingsJson, []byte(secret)) {
			t.Fatal("release snapshot contains plaintext secret")
		}
	}
	values, err := service.vault.Environment(ctx, securestate.RecordRef{Kind: securestate.EnvironmentKind, OwnerID: release.EnvironmentID, Version: release.SecretVersion})
	if err != nil || values["API_TOKEN"] != "captured-private-api" {
		t.Fatalf("selected secret version changed: %v", err)
	}
	retry := input
	retry.Tree = filepath.Join(t.TempDir(), "missing")
	retry.Artifact = nil
	replay, replayedOperation, err := service.QueueRelease(ctx, runtime, resolver, retry)
	if err != nil || replay.ID != release.ID || replayedOperation.ID != operation.ID {
		t.Fatalf("idempotent replay required source or created new release: %v", err)
	}
	metadata, err := service.GetRelease(ctx, release.ID)
	if err != nil || metadata.SecretVersionId != "2" || len(metadata.Images) != 2 {
		t.Fatalf("safe release metadata missing: %v", err)
	}
	services, err := service.ListServices(ctx, release.EnvironmentID, &panelv1.PageRequest{})
	if err != nil || len(services.Services) != 2 {
		t.Fatalf("derived service rows missing: %v", err)
	}
	for _, row := range services.Services {
		if row.State != panelv1.ServiceState_SERVICE_STATE_UNKNOWN || row.Health != panelv1.ServiceHealth_SERVICE_HEALTH_UNKNOWN || row.ObservedReleaseId != "" {
			t.Fatal("prepared metadata falsely reported observed health")
		}
	}
	var releaseCount int
	conflicting := input
	conflicting.Intent.RequestID = uuid.NewString()
	if _, _, err := service.QueueRelease(ctx, runtime, resolver, conflicting); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("conflicting operation admitted: %v", err)
	}
	if err := database.QueryRowContext(ctx, "SELECT count(*) FROM releases").Scan(&releaseCount); err != nil || releaseCount != 1 {
		t.Fatalf("failed admission left orphan release: %v", err)
	}
}
func TestBindingChangeAndDanglingDomainRejectReleaseWithoutEffects(t *testing.T) {
	service, database, runtime, resolver, input := releaseSetup(t)
	ctx := context.Background()
	environment, err := service.GetEnvironment(ctx, input.Selection.Environment.ID)
	if err != nil {
		t.Fatal(err)
	}
	settings := proto.Clone(environment.Settings).(*panelv1.EnvironmentSettings)
	settings.Domains[0].Service = "missing-service"
	updated, err := service.UpdateEnvironment(ctx, &panelv1.UpdateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Id: environment.Id, ExpectedVersion: environment.Version, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	version, err := storeVersion(updated.Environment.Version)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := service.CaptureSelection(ctx, tx, environment.Id, version)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	dangling := input
	dangling.Selection = selection
	dangling.Intent.ExpectedVersion = selection.Environment.Version
	if _, _, err := service.QueueRelease(ctx, runtime, resolver, dangling); !errors.Is(err, ErrInvalid) {
		t.Fatalf("dangling route admitted: %v", err)
	}
	settings.Branch = "release"
	if _, err := service.UpdateEnvironment(ctx, &panelv1.UpdateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Id: environment.Id, ExpectedVersion: updated.Environment.Version, Settings: settings}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.QueueRelease(ctx, runtime, resolver, input); !errors.Is(err, ErrStale) {
		t.Fatalf("stale binding admitted: %v", err)
	}
	var count int
	if err := database.QueryRowContext(ctx, "SELECT count(*) FROM releases").Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid snapshot persisted: %v", err)
	}
}
func TestBindingChangeDuringNativeValidationIsRejectedByFinalTransaction(t *testing.T) {
	service, database, runtime, _, input := releaseSetup(t)
	ctx := context.Background()
	directory := t.TempDir()
	command := os.Getenv("PANEL_TEST_COMPOSE")
	if command == "" {
		command = "docker-compose"
	}
	command, err := exec.LookPath(command)
	if err != nil {
		t.Fatal(err)
	}
	command, err = filepath.Abs(command)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(command, filepath.Join(directory, "real-compose")); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(directory, "slow-compose")
	script := "#!/bin/sh\nset -eu\ntool_dir=$(dirname \"$0\")\nif [ \"$1\" != version ]; then\n  : > \"$tool_dir/ready\"\n  while [ ! -f \"$tool_dir/continue\" ]; do sleep 0.01; done\nfi\nexec \"$tool_dir/real-compose\" \"$@\"\n"
	// Executable mode is required for this trusted test-only synchronization helper.
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil { // #nosec G306 -- fixed local script, owner-only executable.
		t.Fatal(err)
	}
	resolver, err := compose.New(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, _, err := service.QueueRelease(ctx, runtime, resolver, input); result <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(directory, "ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native validation did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	environment, err := service.GetEnvironment(ctx, input.Selection.Environment.ID)
	if err != nil {
		t.Fatal(err)
	}
	settings := proto.Clone(environment.Settings).(*panelv1.EnvironmentSettings)
	settings.Branch = "changed-during-validation"
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := service.UpdateEnvironment(bounded, &panelv1.UpdateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Id: environment.Id, ExpectedVersion: environment.Version, Settings: settings}); err != nil {
		t.Fatalf("native validation held write transaction: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "continue"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrStale) {
			t.Fatalf("final binding recheck missing: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("validation did not finish")
	}
	var count int
	if err := database.QueryRowContext(ctx, "SELECT count(*) FROM releases").Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale release remained: %v", err)
	}
}
func TestDeployedServerBindingCannotBeReassigned(t *testing.T) {
	service, database, runtime, resolver, input := releaseSetup(t)
	ctx := context.Background()
	release, _, err := service.QueueRelease(ctx, runtime, resolver, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "UPDATE environments SET current_release_id=?,applied_version=? WHERE id=?", release.ID, release.EnvironmentVersion, release.EnvironmentID); err != nil {
		t.Fatal(err)
	}
	another := uuid.NewString()
	if err := store.New(database).CreateServer(ctx, store.CreateServerParams{ID: another, Name: "another-generic", TailscaleAddress: another + ".example.ts.net", SshFingerprint: "independently-verified-fixture", CreatedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	environment, err := service.GetEnvironment(ctx, release.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	settings := proto.Clone(environment.Settings).(*panelv1.EnvironmentSettings)
	settings.ServerId = another
	if _, err := service.UpdateEnvironment(ctx, &panelv1.UpdateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Id: environment.Id, ExpectedVersion: environment.Version, Settings: settings}); !errors.Is(err, ErrBinding) {
		t.Fatalf("deployed resources stranded by server edit: %v", err)
	}
}

func TestAutomaticCapturedSelectionCannotQueueAfterAutomationDisabled(t *testing.T) {
	service, database, runtime, resolver, input := releaseSetup(t)
	ctx := context.Background()
	environment, err := service.GetEnvironment(ctx, input.Selection.Environment.ID)
	if err != nil {
		t.Fatal(err)
	}
	settings := proto.Clone(environment.Settings).(*panelv1.EnvironmentSettings)
	settings.AutoDeploy = true
	enabled, err := service.UpdateEnvironment(ctx, &panelv1.UpdateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Id: environment.Id, ExpectedVersion: environment.Version, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	version, err := storeVersion(enabled.Environment.Version)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := service.CaptureSelection(ctx, tx, environment.Id, version)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	settings.AutoDeploy = false
	if _, err := service.UpdateEnvironment(ctx, &panelv1.UpdateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Id: environment.Id, ExpectedVersion: enabled.Environment.Version, Settings: settings}); err != nil {
		t.Fatal(err)
	}
	input.Selection = selection
	input.Intent.ExpectedVersion = selection.Environment.Version
	input.Intent.Automatic = true
	if _, _, err := service.QueueRelease(ctx, runtime, resolver, input); !errors.Is(err, ErrStale) {
		t.Fatalf("automatic captured work ignored disabled setting: %v", err)
	}
}

func TestRepeatedSecretExpansionRejectsOversizedReleaseWithoutStoringPlaintext(t *testing.T) {
	service, database, runtime, resolver, input := releaseSetup(t)
	ctx := context.Background()
	saved, err := service.SaveSecrets(ctx, &panelv1.SaveEnvironmentSecretsRequest{
		Mutation:      &panelv1.Mutation{RequestId: uuid.NewString()},
		EnvironmentId: input.Selection.Environment.ID, ExpectedVersion: 2,
		Upserts: []*panelv1.SecretValue{{Name: "API_TOKEN", Value: strings.Repeat("$", 16<<10)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	version, err := storeVersion(saved.Environment.Version)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	input.Selection, err = service.CaptureSelection(ctx, tx, saved.Environment.Id, version)
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	input.Intent.ExpectedVersion = version
	root, err := os.OpenRoot(input.Tree)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	base, err := root.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var repeated strings.Builder
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&repeated, "      REPEATED_%d: ${API_TOKEN}\n", i)
	}
	base = bytes.Replace(base, []byte("    environment:\n"), []byte("    environment:\n"+repeated.String()), 1)
	if err := root.WriteFile("compose.yaml", base, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.QueueRelease(ctx, runtime, resolver, input); !errors.Is(err, ErrCapacity) {
		t.Fatalf("oversized expanded release admitted: %v", err)
	}
	var count int
	if err := database.QueryRowContext(ctx, "SELECT count(*) FROM releases").Scan(&count); err != nil || count != 0 {
		t.Fatalf("oversized release persisted: %v", err)
	}
}

func TestRouteReservationFailureRollsBackReleaseOperationJobAndClaims(t *testing.T) {
	service, database, runtime, resolver, input := releaseSetup(t)
	if _, err := database.ExecContext(t.Context(), `CREATE TRIGGER reject_route BEFORE INSERT ON release_routes BEGIN SELECT RAISE(ABORT,'fixture route failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.QueueRelease(t.Context(), runtime, resolver, input); err == nil {
		t.Fatal("route failure reported successful admission")
	}
	var count int
	if err := database.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM releases)+(SELECT count(*) FROM operations)+(SELECT count(*) FROM route_claims)+(SELECT count(*) FROM river_job)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("route failure retained partial admission: count=%d error=%v", count, err)
	}
	if _, err := database.ExecContext(t.Context(), `DROP TRIGGER reject_route`); err != nil {
		t.Fatal(err)
	}
	_, operation, err := service.QueueRelease(t.Context(), runtime, resolver, input)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := routes.OperationSnapshot(t.Context(), store.New(database), operation.ID)
	if err != nil || len(snapshot.Bindings) != 1 || snapshot.Bindings[0].Hostname != "app.example.com" {
		t.Fatalf("retry lost captured route ownership: %v", err)
	}
}
func TestCapturedReleaseCannotStealDomainFromNewDesiredEnvironment(t *testing.T) {
	service, database, runtime, resolver, input := releaseSetup(t)
	current, err := service.GetEnvironment(t.Context(), input.Selection.Environment.ID)
	if err != nil {
		t.Fatal(err)
	}
	settings := proto.Clone(current.Settings).(*panelv1.EnvironmentSettings)
	settings.Domains = nil
	if _, err := service.UpdateEnvironment(t.Context(), &panelv1.UpdateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Id: current.Id, ExpectedVersion: current.Version, Settings: settings}); err != nil {
		t.Fatal(err)
	}
	settings.Name = "production"
	settings.Domains = []*panelv1.Domain{{Hostname: "app.example.com", Service: "app", Port: 3000}}
	if _, err := service.CreateEnvironment(t.Context(), &panelv1.CreateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, ProjectId: current.ProjectId, Settings: settings}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.QueueRelease(t.Context(), runtime, resolver, input); !errors.Is(err, routes.ErrOwnership) {
		t.Fatalf("captured old domain stole new desired ownership: %v", err)
	}
	var count int
	if err := database.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM releases)+(SELECT count(*) FROM operations)+(SELECT count(*) FROM route_claims)+(SELECT count(*) FROM river_job)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("conflicting route admission escaped rollback: %v", err)
	}
}
