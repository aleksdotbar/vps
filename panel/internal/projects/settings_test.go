package projects

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/routes"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

func setup(t *testing.T) (*Service, *sql.DB, string) {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "state"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	cipher, err := securestate.NewCipher(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	serverID := uuid.NewString()
	if err := store.New(database).CreateServer(ctx, store.CreateServerParams{ID: serverID, Name: "generic", TailscaleAddress: serverID + ".example.ts.net", SshFingerprint: "independently-verified-fixture", CreatedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	return New(database, cipher), database, serverID
}
func projectAndEnvironment(t *testing.T, service *Service, serverID string) (*panelv1.Project, *panelv1.Environment) {
	t.Helper()
	ctx := context.Background()
	project, err := service.CreateProject(ctx, &panelv1.CreateProjectRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Name: "generic", Repository: &panelv1.Repository{GithubRepositoryId: 1, Owner: "example", Name: "generic"}})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := service.CreateEnvironment(ctx, &panelv1.CreateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, ProjectId: project.Project.Id, Settings: &panelv1.EnvironmentSettings{Name: "staging", ServerId: serverID, Branch: "main", ComposeOverridePath: "compose.staging.yaml", Domains: []*panelv1.Domain{{Hostname: "app.example.com", Service: "app", Port: 3000}}}})
	if err != nil {
		t.Fatal(err)
	}
	return project.Project, environment.Environment
}
func TestSecretEditReplayIsProtectedAndAtomic(t *testing.T) {
	ctx := context.Background()
	service, database, serverID := setup(t)
	_, environment := projectAndEnvironment(t, service, serverID)
	request := &panelv1.SaveEnvironmentSecretsRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, EnvironmentId: environment.Id, ExpectedVersion: environment.Version, Upserts: []*panelv1.SecretValue{{Name: "WORKER_TOKEN", Value: "worker-private-value"}, {Name: "API_TOKEN", Value: "api-private-value"}}}
	first, err := service.SaveSecrets(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.SaveSecrets(ctx, request)
	if err != nil || !proto.Equal(first, replay) {
		t.Fatalf("retry changed immutable mutation result: %v", err)
	}
	current, err := store.New(database).GetEnvironment(ctx, environment.Id)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != 2 || current.SecretVersion != 2 || current.BindingVersion != 1 || current.CurrentReleaseID.Valid {
		t.Fatal("secret save changed running/binding state or replayed write")
	}
	stored, err := store.New(database).GetMetadataMutation(ctx, request.Mutation.RequestId)
	if err != nil {
		t.Fatal(err)
	}
	canonical := proto.Clone(request).(*panelv1.SaveEnvironmentSecretsRequest)
	canonical.Mutation = nil
	sort.Slice(canonical.Upserts, func(i, j int) bool { return canonical.Upserts[i].GetName() < canonical.Upserts[j].GetName() })
	sort.Strings(canonical.RemoveNames)
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	plainHash := sha256.Sum256(encoded)
	if bytes.Equal(stored.Identity, plainHash[:]) || bytes.Contains(stored.EncryptedResponse, []byte("api-private-value")) {
		t.Fatal("secret replay record exposes plaintext/hash oracle")
	}
	altered := proto.Clone(request).(*panelv1.SaveEnvironmentSecretsRequest)
	altered.Upserts[0].Value = "different-private-value"
	if _, err := service.SaveSecrets(ctx, altered); !errors.Is(err, ErrReused) {
		t.Fatalf("changed secret payload reused request identity: %v", err)
	}
	values, err := service.vault.Environment(ctx, securestate.RecordRef{Kind: securestate.EnvironmentKind, OwnerID: environment.Id, Version: 2})
	if err != nil || values["API_TOKEN"] != "api-private-value" {
		t.Fatalf("secret version changed: %v", err)
	}
	var operationsCount int
	if err := database.QueryRowContext(ctx, "SELECT count(*) FROM operations").Scan(&operationsCount); err != nil || operationsCount != 0 {
		t.Fatalf("secret edit enqueued execution: %v", err)
	}
}
func TestEnvironmentCreationFailureRollsBackEncryptedVersionAndReplay(t *testing.T) {
	ctx := context.Background()
	service, database, serverID := setup(t)
	project, environment := projectAndEnvironment(t, service, serverID)
	requestID := uuid.NewString()
	request := &panelv1.CreateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: requestID}, ProjectId: project.Id, Settings: proto.Clone(environment.Settings).(*panelv1.EnvironmentSettings)}
	request.Settings.Name = "production"
	if _, err := service.CreateEnvironment(ctx, request); !errors.Is(err, ErrConflict) {
		t.Fatalf("hostname ownership conflict accepted: %v", err)
	}
	var versions, environments int
	if err := database.QueryRowContext(ctx, "SELECT count(*) FROM encrypted_versions").Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, "SELECT count(*) FROM environments").Scan(&environments); err != nil {
		t.Fatal(err)
	}
	if versions != 1 || environments != 1 {
		t.Fatal("failed create left orphan encrypted state")
	}
	if _, err := store.New(database).GetMetadataMutation(ctx, requestID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed mutation identity was committed: %v", err)
	}
}
func TestBindingEditSupersedesQueuedWorkButDomainAndSecretEditsDoNot(t *testing.T) {
	ctx := context.Background()
	service, database, serverID := setup(t)
	_, environment := projectAndEnvironment(t, service, serverID)
	runtime, err := operations.New(ctx, database, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	intent := operations.Intent{RequestID: uuid.NewString(), Kind: operations.Deploy, EnvironmentID: environment.Id, ExpectedVersion: 1, SourceSHA: "0123456789abcdef0123456789abcdef01234567"}
	admitted, err := runtime.Enqueue(ctx, tx, intent, operations.Assignment{ServerID: serverID, EnvironmentID: environment.Id, ReleaseID: uuid.NewString(), BindingVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	saved, err := service.SaveSecrets(ctx, &panelv1.SaveEnvironmentSecretsRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, EnvironmentId: environment.Id, ExpectedVersion: 1, Upserts: []*panelv1.SecretValue{{Name: "API_TOKEN", Value: "pending-private-value"}}})
	if err != nil {
		t.Fatal(err)
	}
	settings := proto.Clone(saved.Environment.Settings).(*panelv1.EnvironmentSettings)
	settings.Domains[0].Hostname = "new.example.com"
	edited, err := service.UpdateEnvironment(ctx, &panelv1.UpdateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Id: environment.Id, ExpectedVersion: saved.Environment.Version, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := store.New(database).GetOperation(ctx, admitted.ID)
	if err != nil || queued.State != "queued" || edited.Environment.BindingVersion != 1 {
		t.Fatalf("pending domain/secret edit changed admitted binding: %v", err)
	}
	settings.Branch = "release"
	binding, err := service.UpdateEnvironment(ctx, &panelv1.UpdateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Id: environment.Id, ExpectedVersion: edited.Environment.Version, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	queued, err = store.New(database).GetOperation(ctx, admitted.ID)
	if err != nil || queued.State != "superseded" || binding.Environment.BindingVersion != 2 {
		t.Fatalf("old queued binding remained executable: %v", err)
	}
	if _, err := service.UpdateEnvironment(ctx, &panelv1.UpdateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Id: environment.Id, ExpectedVersion: 1, Settings: settings}); !errors.Is(err, ErrStale) {
		t.Fatalf("stale settings accepted: %v", err)
	}
}
func TestListCursorIsStableAndFilterBound(t *testing.T) {
	ctx := context.Background()
	service, _, serverID := setup(t)
	project, environment := projectAndEnvironment(t, service, serverID)
	request := &panelv1.CreateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, ProjectId: project.Id, Settings: proto.Clone(environment.Settings).(*panelv1.EnvironmentSettings)}
	request.Settings.Name = "production"
	request.Settings.Domains = nil
	if _, err := service.CreateEnvironment(ctx, request); err != nil {
		t.Fatal(err)
	}
	first, err := service.ListEnvironments(ctx, project.Id, &panelv1.PageRequest{PageSize: 1})
	if err != nil || len(first.Environments) != 1 || first.Page.NextPageToken == "" {
		t.Fatalf("first page missing: %v", err)
	}
	second, err := service.ListEnvironments(ctx, project.Id, &panelv1.PageRequest{PageSize: 1, PageToken: first.Page.NextPageToken})
	if err != nil || len(second.Environments) != 1 || first.Environments[0].Id == second.Environments[0].Id {
		t.Fatalf("keyset page repeated/lost row: %v", err)
	}
	if _, err := service.ListEnvironments(ctx, uuid.NewString(), &panelv1.PageRequest{PageToken: first.Page.NextPageToken}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cursor crossed scope: %v", err)
	}
}

func TestDisablingAutomationSupersedesOnlyQueuedAutomaticIntent(t *testing.T) {
	ctx := context.Background()
	service, database, serverID := setup(t)
	_, environment := projectAndEnvironment(t, service, serverID)
	settings := proto.Clone(environment.Settings).(*panelv1.EnvironmentSettings)
	settings.AutoDeploy = true
	enabled, err := service.UpdateEnvironment(ctx, &panelv1.UpdateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Id: environment.Id, ExpectedVersion: 1, Settings: settings})
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
	version, err := storeVersion(enabled.Environment.Version)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := runtime.Enqueue(ctx, tx, operations.Intent{RequestID: uuid.NewString(), Kind: operations.Deploy, EnvironmentID: environment.Id, ExpectedVersion: version, SourceSHA: "0123456789abcdef0123456789abcdef01234567", Automatic: true}, operations.Assignment{ServerID: serverID, EnvironmentID: environment.Id, ReleaseID: uuid.NewString(), BindingVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	settings.AutoDeploy = false
	if _, err := service.UpdateEnvironment(ctx, &panelv1.UpdateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Id: environment.Id, ExpectedVersion: enabled.Environment.Version, Settings: settings}); err != nil {
		t.Fatal(err)
	}
	operation, err := store.New(database).GetOperation(ctx, queued.ID)
	if err != nil || operation.State != "superseded" || operation.Started {
		t.Fatalf("automatic queued work survived disable: %v", err)
	}
}

func TestSettingsCannotTransferAdmittedHostnameOrMoveItsEnvironment(t *testing.T) {
	service, database, runtime, resolver, input := releaseSetup(t)
	if _, _, err := service.QueueRelease(t.Context(), runtime, resolver, input); err != nil {
		t.Fatal(err)
	}
	current, err := service.GetEnvironment(t.Context(), input.Selection.Environment.ID)
	if err != nil {
		t.Fatal(err)
	}
	settings := proto.Clone(current.Settings).(*panelv1.EnvironmentSettings)
	settings.Domains = nil
	updated, err := service.UpdateEnvironment(t.Context(), &panelv1.UpdateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Id: current.Id, ExpectedVersion: current.Version, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	otherSettings := proto.Clone(settings).(*panelv1.EnvironmentSettings)
	otherSettings.Name = "production"
	otherSettings.Domains = []*panelv1.Domain{{Hostname: "app.example.com", Service: "app", Port: 3000}}
	if _, err := service.CreateEnvironment(t.Context(), &panelv1.CreateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, ProjectId: current.ProjectId, Settings: otherSettings}); !errors.Is(err, routes.ErrOwnership) {
		t.Fatalf("removing desired hostname transferred admitted claim: %v", err)
	}
	otherServer := uuid.NewString()
	if err := store.New(database).CreateServer(t.Context(), store.CreateServerParams{ID: otherServer, Name: "other", TailscaleAddress: otherServer + ".example.ts.net", SshFingerprint: "fixture", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	settings.ServerId = otherServer
	if _, err := service.UpdateEnvironment(t.Context(), &panelv1.UpdateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Id: current.Id, ExpectedVersion: updated.Environment.Version, Settings: settings}); !errors.Is(err, routes.ErrOwnership) {
		t.Fatalf("domain removal allowed rebind of admitted owner: %v", err)
	}
	current, err = service.GetEnvironment(t.Context(), current.Id)
	if err != nil || current.Settings.ServerId != input.Selection.Environment.ServerID {
		t.Fatalf("failed ownership edit mutated binding: %v", err)
	}
}

func TestSourceVariableCannotBeSavedAsSecret(t *testing.T) {
	ctx := context.Background()
	service, _, serverID := setup(t)
	_, environment := projectAndEnvironment(t, service, serverID)
	request := &panelv1.SaveEnvironmentSecretsRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, EnvironmentId: environment.Id, ExpectedVersion: environment.Version, Upserts: []*panelv1.SecretValue{{Name: "GIT_SHA", Value: "0123456789"}}}
	if _, err := service.SaveSecrets(ctx, request); !errors.Is(err, ErrInvalid) {
		t.Fatalf("GIT_SHA secret accepted: %v", err)
	}
}
