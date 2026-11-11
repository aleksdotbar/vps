package githubflow

import (
	"bytes"
	"database/sql"
	"errors"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v93/github"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/projects"
	"github.com/aleksdotbar/vps/panel/internal/routes"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

func retainedRollbackRelease(t *testing.T, f *buildFixture, environment, server string, domains []byte) store.Release {
	t.Helper()
	id := uuid.NewString()
	queries := store.New(f.service.database)
	if err := queries.CreateRelease(t.Context(), store.CreateReleaseParams{ID: id, EnvironmentID: environment, ServerID: server, RepositoryID: 1, WorkflowID: 2, RunID: 107, RunAttempt: 1, Branch: "previous-branch", SourceSha: fixtureSHA, EnvironmentVersion: 1, BindingVersion: 1, ImagesJson: []byte(`[{"service":"app","image_digest":"ghcr.io/example/generic@sha256:` + strings.Repeat("a", 64) + `"}]`), DomainsJson: domains, SecretVersion: 1, ComposeConfig: []byte(`{"services":{"app":{"image":"ghcr.io/example/generic@sha256:` + strings.Repeat("a", 64) + `"}}}`), SecretBindingsJson: []byte(`[]`), ConfigSha256: strings.Repeat("a", 64), CreatedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := queries.CreateReleaseService(t.Context(), store.CreateReleaseServiceParams{ReleaseID: id, Name: "app", ImageDigest: "ghcr.io/example/generic@sha256:" + strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	release, err := queries.GetRelease(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return release
}

func rollbackRequest(f *buildFixture, release string) *panelv1.RollbackEnvironmentRequest {
	return &panelv1.RollbackEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, EnvironmentId: f.environment.Id, ExpectedVersion: f.environment.Version, ReleaseId: release}
}

func TestRollbackAdmissionRetainsSnapshotSupersedesBuildsAndReplaysOffline(t *testing.T) {
	f := buildSetup(t)
	enableAutomatic(t, f)
	ctx := t.Context()
	queries := store.New(f.service.database)
	release := retainedRollbackRelease(t, f, f.environment.Id, f.environment.Settings.ServerId, []byte(`[]`))
	settings := proto.Clone(f.environment.Settings).(*panelv1.EnvironmentSettings)
	settings.ComposeOverridePath = "compose.other.yaml"
	changed, err := f.metadata.UpdateEnvironment(ctx, &panelv1.UpdateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Id: f.environment.Id, ExpectedVersion: f.environment.Version, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	f.environment = changed.Environment
	if f.environment.Version > math.MaxInt64 {
		t.Fatal("fixture environment version exceeds database range")
		return
	}
	waitingID := f.request(t)
	autoID := uuid.NewString()
	tx, err := f.service.database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.runtime.Enqueue(ctx, tx, operations.Intent{RequestID: autoID, Kind: operations.Deploy, EnvironmentID: f.environment.Id, ExpectedVersion: int64(f.environment.Version), SourceSHA: fixtureSHA, Automatic: true}, operations.Assignment{ServerID: release.ServerID, EnvironmentID: release.EnvironmentID, ReleaseID: release.ID, BindingVersion: 2})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// The provider knows a run discovery has never persisted, still in progress.
	run := fixtureRun(99, "push")
	run.Status, run.Conclusion = new("in_progress"), nil
	f.latest = []*github.WorkflowRun{run}
	request := rollbackRequest(f, release.ID)
	id, err := f.builds.RequestRollback(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := queries.GetOperation(ctx, id)
	if err != nil || operation.Kind != "rollback" || operation.BindingVersion != 2 || operation.ReleaseID != release.ID || operation.RiverJobID < 1 || operation.State != "queued" {
		t.Fatalf("rollback operation/job missing: %+v %v", operation, err)
	}
	retained, err := queries.GetRelease(ctx, release.ID)
	if err != nil || !reflect.DeepEqual(retained, release) {
		t.Fatalf("rollback changed retained material: %v", err)
	}
	old, err := queries.GetOperation(ctx, autoID)
	if err != nil || old.State != "superseded" {
		t.Fatalf("queued automatic work survived: %v", err)
	}
	waiting, err := queries.GetWaitingBuild(ctx, waitingID)
	if err != nil || waiting.State != "superseded" {
		t.Fatalf("manual build wait survived latest intent: %v", err)
	}
	progress, err := queries.GetBranchProgress(ctx, f.environment.Id)
	if err != nil || progress.ManualCutoff != 99 || progress.CandidateRunID != 0 {
		t.Fatalf("unseen provider cutoff missing: %+v %v", progress, err)
	}
	f.service.transport = fixtureTransport(func(*http.Request) (*http.Response, error) {
		t.Error("replay/superseded poll contacted provider")
		return nil, ErrProvider
	})
	if delay, err := f.builds.PollBuild(ctx, waitingID); err != nil || delay != 0 {
		t.Fatalf("superseded waiter promoted or retried: %v", err)
	}
	if replay, err := f.builds.RequestRollback(ctx, request); err != nil || replay != id {
		t.Fatalf("offline replay failed: %v", err)
	}
	request.ExpectedVersion++
	if _, err := f.builds.RequestRollback(ctx, request); !errors.Is(err, operations.ErrRequestReused) {
		t.Fatalf("changed identity accepted: %v", err)
	}
	// Once the admitted rollback finishes, an already existing older run still
	// cannot become a new automatic deployment. No executor success is claimed.
	if _, err := f.service.database.ExecContext(ctx, `UPDATE operations SET state='failed',outcome='failure' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	f.service.transport = f.respond(t)
	f.latest = []*github.WorkflowRun{fixtureRun(98, "push")}
	discoveryDue(t, f)
	if err := f.builds.Discover(ctx, f.environment.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.ActiveWaitingBuildForEnvironment(ctx, f.environment.Id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old provider run undid rollback: %v", err)
	}
}

func TestRollbackAdmissionRejectsForeignReleaseAndStaleIntentBeforeProvider(t *testing.T) {
	f := buildSetup(t)
	ctx := t.Context()
	other, err := f.metadata.CreateEnvironment(ctx, &panelv1.CreateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, ProjectId: f.environment.ProjectId, Settings: &panelv1.EnvironmentSettings{Name: "other", ServerId: f.environment.Settings.ServerId, Branch: "main", ComposeOverridePath: "compose.staging.yaml"}})
	if err != nil {
		t.Fatal(err)
	}
	server := uuid.NewString()
	if err := store.New(f.service.database).CreateServer(ctx, store.CreateServerParams{ID: server, Name: "other", TailscaleAddress: "other.example.ts.net", SshFingerprint: "independent-fixture", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	valid := retainedRollbackRelease(t, f, f.environment.Id, f.environment.Settings.ServerId, []byte(`[]`))
	foreign := retainedRollbackRelease(t, f, other.Environment.Id, f.environment.Settings.ServerId, []byte(`[]`))
	moved := retainedRollbackRelease(t, f, f.environment.Id, server, []byte(`[]`))
	f.service.transport = fixtureTransport(func(*http.Request) (*http.Response, error) {
		t.Error("invalid rollback reached provider")
		return nil, ErrProvider
	})
	for _, test := range []struct {
		name, release string
		version       uint64
		want          error
	}{{"foreign environment", foreign.ID, f.environment.Version, projects.ErrBinding}, {"foreign server", moved.ID, f.environment.Version, projects.ErrBinding}, {"missing", uuid.NewString(), f.environment.Version, projects.ErrNotFound}, {"stale", valid.ID, f.environment.Version + 1, projects.ErrStale}} {
		t.Run(test.name, func(t *testing.T) {
			request := rollbackRequest(f, test.release)
			request.ExpectedVersion = test.version
			if _, err := f.builds.RequestRollback(ctx, request); !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}
}

func TestRollbackAdmissionRollsBackCutoffSupersessionAndJobOnConflict(t *testing.T) {
	for _, reason := range []string{"started unknown", "missing retained routes", "version changed during provider"} {
		t.Run(reason, func(t *testing.T) {
			f := buildSetup(t)
			ctx := t.Context()
			queries := store.New(f.service.database)
			domains := []byte(`[]`)
			if reason == "missing retained routes" {
				domains = []byte(`[{"hostname":"old.example.com","service":"app","port":8080}]`)
			}
			release := retainedRollbackRelease(t, f, f.environment.Id, f.environment.Settings.ServerId, domains)
			waitingID := f.request(t)
			if reason == "started unknown" {
				if f.environment.Version > math.MaxInt64 {
					t.Fatal("fixture environment version exceeds database range")
					return
				}
				tx, err := f.service.database.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				op, err := f.runtime.Enqueue(ctx, tx, operations.Intent{RequestID: uuid.NewString(), Kind: operations.Deploy, EnvironmentID: f.environment.Id, ExpectedVersion: int64(f.environment.Version), SourceSHA: fixtureSHA, Automatic: true}, operations.Assignment{ServerID: release.ServerID, EnvironmentID: release.EnvironmentID, ReleaseID: release.ID, BindingVersion: 1})
				if err != nil {
					_ = tx.Rollback()
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				if _, err := f.service.database.ExecContext(ctx, `UPDATE operations SET started=TRUE,state='needs_attention',outcome='unknown' WHERE id=?`, op.ID); err != nil {
					t.Fatal(err)
				}
			}
			before, err := queries.OperationCapacity(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var jobs int
			if err := f.service.database.QueryRowContext(ctx, `SELECT count(*) FROM river_job`).Scan(&jobs); err != nil {
				t.Fatal(err)
			}
			f.latest = []*github.WorkflowRun{fixtureRun(99, "push")}
			if reason == "version changed during provider" {
				base := f.respond(t)
				f.service.transport = fixtureTransport(func(request *http.Request) (*http.Response, error) {
					if strings.HasSuffix(request.URL.Path, "/runs") {
						_, err := f.metadata.SaveSecrets(ctx, &panelv1.SaveEnvironmentSecretsRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, EnvironmentId: f.environment.Id, ExpectedVersion: f.environment.Version, Upserts: []*panelv1.SecretValue{{Name: "API_TOKEN", Value: "pending-private"}}})
						if err != nil {
							t.Fatal(err)
						}
					}
					return base.RoundTrip(request)
				})
			}
			request := rollbackRequest(f, release.ID)
			_, err = f.builds.RequestRollback(ctx, request)
			want := operations.ErrConflict
			if reason == "missing retained routes" {
				want = routes.ErrInvalid
			}
			if reason == "version changed during provider" {
				want = projects.ErrStale
			}
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			after, err := queries.OperationCapacity(ctx)
			if err != nil || after != before {
				t.Fatalf("failed admission left operation: %v", err)
			}
			var retainedJobs int
			if err := f.service.database.QueryRowContext(ctx, `SELECT count(*) FROM river_job`).Scan(&retainedJobs); err != nil || retainedJobs != jobs {
				t.Fatalf("failed admission left job: %v", err)
			}
			waiting, err := queries.GetWaitingBuild(ctx, waitingID)
			if err != nil || waiting.State != "waiting" {
				t.Fatalf("failed admission superseded wait: %v", err)
			}
			progress, err := queries.GetBranchProgress(ctx, f.environment.Id)
			if err != nil || progress.ManualCutoff != 0 {
				t.Fatalf("failed admission changed cutoff: %v", err)
			}
			if _, err := queries.GetOperation(ctx, request.Mutation.RequestId); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("failed intent survived: %v", err)
			}
			if bytes.Contains(release.ComposeConfig, []byte("pending-private")) {
				t.Fatal("rollback contains pending secret")
			}
		})
	}
}
