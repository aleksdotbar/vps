package githubflow

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-github/v93/github"
	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riversqlite"
	"github.com/riverqueue/river/rivertype"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/compose"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/projects"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

const fixtureSHA = "0123456789abcdef0123456789abcdef01234567"

type buildFixture struct {
	service            *Service
	metadata           *projects.Service
	builds             *Builds
	runtime            *operations.Runtime
	environment        *panelv1.Environment
	files              map[string][]byte
	archive            []byte
	digest             string
	runs               []*github.WorkflowRun
	latest             []*github.WorkflowRun
	requestID          string
	dispatches         atomic.Int64
	lostDispatch       bool
	dispatched         atomic.Bool
	tokenMints         atomic.Int64
	sourceReads        atomic.Int64
	failDiscovery      func()
	dispatchConclusion string
}

func buildSetup(t *testing.T) *buildFixture {
	t.Helper()
	ctx := context.Background()
	service := setupService(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := json.Marshal(appCredential{ClientID: "Iv1.generic", PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := service.vault.SaveCredential(ctx, "github-app", 0, credential)
	if err != nil {
		t.Fatal(err)
	}
	queries := store.New(service.database)
	if err := queries.CreateGitHubIntegration(ctx, store.CreateGitHubIntegrationParams{AppID: 2, OwnerID: 1, OwnerLogin: "operator", AppSlug: "generic", CredentialVersion: ref.Version}); err != nil {
		t.Fatal(err)
	}
	if err := queries.SetGitHubInstallation(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if err := queries.CreateGitHubRepository(ctx, store.CreateGitHubRepositoryParams{ID: 1, Owner: "operator", Name: "generic"}); err != nil {
		t.Fatal(err)
	}
	serverID := uuid.NewString()
	if err := queries.CreateServer(ctx, store.CreateServerParams{ID: serverID, Name: "generic", TailscaleAddress: "generic.example.ts.net", SshFingerprint: "independently-verified-fixture", CreatedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := queries.ObserveServer(ctx, store.ObserveServerParams{ID: serverID, Readiness: "ready", ObservedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	metadata := projects.New(service.database, service.cipher)
	project, err := metadata.CreateProject(ctx, &panelv1.CreateProjectRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Name: "generic", Repository: &panelv1.Repository{GithubRepositoryId: 1, Owner: "operator", Name: "generic"}, GithubWorkflowId: 2})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := metadata.CreateEnvironment(ctx, &panelv1.CreateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, ProjectId: project.Project.Id, Settings: &panelv1.EnvironmentSettings{Name: "staging", ServerId: serverID, Branch: "main", ComposeOverridePath: "compose.staging.yaml"}})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := metadata.SaveSecrets(ctx, &panelv1.SaveEnvironmentSecretsRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, EnvironmentId: environment.Environment.Id, ExpectedVersion: 1, Upserts: []*panelv1.SecretValue{{Name: "API_TOKEN", Value: "captured-private-api"}, {Name: "WORKER_TOKEN", Value: "captured-private-worker"}}})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &buildFixture{service: service, metadata: metadata, environment: saved.Environment, files: make(map[string][]byte)}
	root, err := os.OpenRoot("../../../examples/stateless")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	for _, name := range []string{"compose.yaml", "compose.staging.yaml", "release.json"} {
		data, err := root.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		fixture.files[name] = data
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	file, err := writer.Create("release.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(fixture.files["release.json"]); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.archive = archive.Bytes()
	hash := sha256.Sum256(fixture.archive)
	fixture.digest = "sha256:" + hex.EncodeToString(hash[:])
	service.transport = fixture.respond(t)
	service.artifactTransport = fixtureTransport(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "" {
			t.Fatal("artifact download received authentication")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(fixture.archive)), Request: request}, nil
	})
	command := os.Getenv("PANEL_TEST_COMPOSE")
	if command == "" {
		command = "docker-compose"
	}
	resolver, err := compose.New(command)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := operations.New(ctx, service.database, nil, func(workers *river.Workers, runtime *operations.Runtime) ([]*river.PeriodicJob, error) {
		fixture.builds = NewBuilds(service, metadata, resolver, runtime)
		return fixture.builds.Register(workers)
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.runtime = runtime
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = fixture.runtime.Stop(ctx)
	})
	return fixture
}
func fixtureRun(number int, event string) *github.WorkflowRun {
	id := int64(100 + number)
	repository := &github.Repository{ID: new(int64(1))}
	return &github.WorkflowRun{ID: &id, RunNumber: &number, RunAttempt: new(1), WorkflowID: new(int64(2)), Repository: repository, HeadRepository: repository, HeadBranch: new("main"), HeadSHA: new(fixtureSHA), Path: new(".github/workflows/panel-build.yaml"), Event: &event, Status: new("completed"), Conclusion: new("success")}
}
func (f *buildFixture) respond(t *testing.T) http.RoundTripper {
	return fixtureTransport(func(request *http.Request) (*http.Response, error) {
		var value any
		status := http.StatusOK
		headers := make(http.Header)
		switch path := request.URL.Path; {
		case path == "/app/installations/10/access_tokens":
			f.tokenMints.Add(1)
			value = map[string]any{"token": uuid.NewString(), "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
		case path == "/repos/operator/generic/actions/workflows/2":
			value = map[string]any{"id": 2, "path": ".github/workflows/panel-build.yaml", "state": "active"}
		case path == "/repos/operator/generic/actions/workflows/2/runs":
			if request.URL.Query().Get("event") == "push" && f.failDiscovery != nil {
				f.failDiscovery()
				return nil, errors.New("fixture provider unavailable")
			}
			runs := f.runs
			if request.URL.Query().Get("head_sha") == "" {
				runs = f.latest
			}
			if f.dispatched.Load() {
				run := fixtureRun(7, "workflow_dispatch")
				run.DisplayTitle = new("panel-build " + f.requestID)
				if f.dispatchConclusion != "" {
					run.Conclusion = new(f.dispatchConclusion)
				}
				runs = []*github.WorkflowRun{run}
			}
			value = map[string]any{"total_count": len(runs), "workflow_runs": runs}
		case path == "/repos/operator/generic/branches/main":
			value = map[string]any{"name": "main", "commit": map[string]any{"sha": fixtureSHA}}
		case path == "/repos/operator/generic/actions/workflows/2/dispatches":
			f.dispatches.Add(1)
			var body struct {
				Inputs map[string]string `json:"inputs"`
				Return bool              `json:"return_run_details"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if !body.Return {
				t.Fatal("dispatch did not request native run details")
			}
			f.requestID = body.Inputs["request_id"]
			f.dispatched.Store(true)
			if f.lostDispatch {
				return nil, errors.New("fixture dispatch response lost")
			}
			value = map[string]any{"workflow_run_id": 107}
		case strings.Contains(path, "/actions/runs/") && (strings.HasSuffix(path, "/attempts/1") || path == "/repos/operator/generic/actions/runs/107"):
			run := fixtureRun(7, "push")
			if f.dispatched.Load() {
				run.Event = new("workflow_dispatch")
				run.DisplayTitle = new("panel-build " + f.requestID)
				if f.dispatchConclusion != "" {
					run.Conclusion = new(f.dispatchConclusion)
				}
			}
			value = run
		case path == "/repos/operator/generic/actions/runs/107/artifacts":
			value = map[string]any{"total_count": 1, "artifacts": []any{map[string]any{"id": 50, "name": "panel-release-107-1", "size_in_bytes": len(f.archive), "digest": f.digest, "expired": false, "workflow_run": map[string]any{"id": 107, "repository_id": 1, "head_repository_id": 1, "head_branch": "main", "head_sha": fixtureSHA}}}}
		case path == "/repos/operator/generic/actions/artifacts/50/zip":
			status = http.StatusFound
			headers.Set("Location", "https://artifacts.actions.githubusercontent.com/generic.zip")
			value = map[string]any{}
		case strings.HasPrefix(path, "/repos/operator/generic/contents/"):
			f.sourceReads.Add(1)
			if request.URL.Query().Get("ref") != fixtureSHA {
				t.Fatal("source read used moving ref")
			}
			name := strings.TrimPrefix(path, "/repos/operator/generic/contents/")
			content, exists := f.files[name]
			if !exists {
				t.Fatal("unexpected source file")
			}
			value = map[string]any{"type": "file", "path": name, "size": len(content), "encoding": "base64", "content": base64.StdEncoding.EncodeToString(content)}
		default:
			t.Fatalf("unexpected GitHub provider request %s", request.URL.Path)
		}
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(bytes.NewReader(data)), Request: request}, nil
	})
}
func (f *buildFixture) request(t *testing.T) string {
	t.Helper()
	id, err := f.builds.RequestDeploy(context.Background(), &panelv1.DeployEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, EnvironmentId: f.environment.Id, ExpectedVersion: f.environment.Version, SourceSha: fixtureSHA})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func (f *buildFixture) due(t *testing.T, id string) {
	t.Helper()
	if err := store.New(f.service.database).ScheduleWaitingBuild(context.Background(), store.ScheduleWaitingBuildParams{ID: id, NextPollAt: 0, DiscoveryPage: 1, Failures: 0, UpdatedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
}
func TestVerifiedBuildPromotesAtomicImmutableReleaseAndReplaysWithoutProvider(t *testing.T) {
	f := buildSetup(t)
	ctx := context.Background()
	f.runs = []*github.WorkflowRun{fixtureRun(7, "push")}
	id := f.request(t)
	if _, err := f.metadata.SaveSecrets(ctx, &panelv1.SaveEnvironmentSecretsRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, EnvironmentId: f.environment.Id, ExpectedVersion: f.environment.Version, Upserts: []*panelv1.SecretValue{{Name: "API_TOKEN", Value: "later-pending-private-api"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.builds.PollBuild(ctx, id); err != nil {
		t.Fatal(err)
	}
	f.due(t, id)
	if delay, err := f.builds.PollBuild(ctx, id); err != nil || delay != 0 {
		t.Fatalf("verified promotion failed: %v", err)
	}
	operation, err := store.New(f.service.database).GetOperation(ctx, id)
	if err != nil || operation.ReleaseID == "" {
		t.Fatalf("stable operation missing: %v", err)
	}
	waiting, err := store.New(f.service.database).GetWaitingBuild(ctx, id)
	if err != nil || waiting.State != "queued" || waiting.ArtifactID != 50 || waiting.ArtifactDigest != f.digest {
		t.Fatalf("artifact/operation not atomically bound: %v", err)
	}
	release, err := store.New(f.service.database).GetRelease(ctx, operation.ReleaseID)
	if err != nil || release.SecretVersion != 2 {
		t.Fatalf("captured secret selection changed: %v", err)
	}
	if bytes.Contains(release.ComposeConfig, []byte("captured-private")) || bytes.Contains(release.ComposeConfig, []byte("later-pending")) {
		t.Fatal("plaintext persisted in release")
	}
	if f.dispatches.Load() != 0 || f.sourceReads.Load() != 2 || f.tokenMints.Load() != 1 {
		t.Fatalf("unexpected provider work dispatch%d/source%d/token%d", f.dispatches.Load(), f.sourceReads.Load(), f.tokenMints.Load())
	}
	f.service.transport = fixtureTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("replay reached provider")
		return nil, ErrProvider
	})
	request := &panelv1.DeployEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: id}, EnvironmentId: f.environment.Id, ExpectedVersion: f.environment.Version, SourceSha: fixtureSHA}
	if replay, err := f.builds.RequestDeploy(ctx, request); err != nil || replay != id {
		t.Fatalf("stable replay failed: %v", err)
	}
	request.SourceSha = strings.Repeat("a", 40)
	if _, err := f.builds.RequestDeploy(ctx, request); !errors.Is(err, operations.ErrRequestReused) {
		t.Fatalf("changed intent reused identity: %v", err)
	}
}
func TestLostDispatchResponseSurvivesNativeRuntimeRestartWithoutSecondDispatch(t *testing.T) {
	f := buildSetup(t)
	ctx := context.Background()
	f.lostDispatch = true
	id := f.request(t)
	if err := f.runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		waiting, err := store.New(f.service.database).GetWaitingBuild(ctx, id)
		if err == nil && waiting.State == "dispatch_unknown" && waiting.Failures > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("first runtime did not persist unknown dispatch: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if err := f.runtime.Stop(stopCtx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	f.due(t, id)
	restarted, err := operations.New(ctx, f.service.database, nil, func(workers *river.Workers, runtime *operations.Runtime) ([]*river.PeriodicJob, error) {
		f.builds = NewBuilds(f.service, f.metadata, f.builds.resolver, runtime)
		return f.builds.Register(workers)
	})
	if err != nil {
		t.Fatal(err)
	}
	f.runtime = restarted
	writer, err := river.NewClient(riversqlite.New(f.service.database), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := store.New(f.service.database).GetWaitingBuild(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.JobRetry(ctx, waiting.RiverJobID); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Start(ctx); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(8 * time.Second)
	for {
		waiting, err = store.New(f.service.database).GetWaitingBuild(ctx, id)
		if err == nil && waiting.RunID == 107 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restart did not reconcile dispatched run: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	stopCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
	if err := restarted.Stop(stopCtx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	f.due(t, id)
	if delay, err := f.builds.PollBuild(ctx, id); err != nil || delay != 0 {
		t.Fatalf("reconciled build did not promote: %v", err)
	}
	waiting, err = store.New(f.service.database).GetWaitingBuild(ctx, id)
	if err != nil || waiting.State != "queued" {
		t.Fatalf("dispatched build was not queued: %v, state%s", err, waiting.State)
	}
	operation, err := store.New(f.service.database).GetOperation(ctx, id)
	if err != nil || operation.ReleaseID == "" {
		t.Fatalf("dispatched build release missing: %v", err)
	}
	if _, err := store.New(f.service.database).GetRelease(ctx, operation.ReleaseID); err != nil {
		t.Fatal(err)
	}
	if f.dispatches.Load() != 1 {
		t.Fatalf("dispatch repeated %d times", f.dispatches.Load())
	}
}

// Keep only real domain rows and native River APIs in restart tests; no copied
// River SQL or assertions about its lease/reclaim implementation belong here.

func TestLostDispatchBindsKnownFailedRunWithoutRedispatch(t *testing.T) {
	f := buildSetup(t)
	ctx := context.Background()
	f.lostDispatch = true
	f.dispatchConclusion = "failure"
	id := f.request(t)
	if _, err := f.builds.PollBuild(ctx, id); err != nil {
		t.Fatal(err)
	}
	f.due(t, id)
	if _, err := f.builds.PollBuild(ctx, id); err != nil {
		t.Fatal(err)
	}
	f.due(t, id)
	if _, err := f.builds.PollBuild(ctx, id); err != nil {
		t.Fatal(err)
	}
	waiting, err := store.New(f.service.database).GetWaitingBuild(ctx, id)
	if err != nil || waiting.State != "failed" || waiting.Reason != "build_failed" {
		t.Fatalf("known failed build remained uncertain: %v, %s/%s", err, waiting.State, waiting.Reason)
	}
	if f.dispatches.Load() != 1 {
		t.Fatal("failed dispatch replayed")
	}
	if _, err := store.New(f.service.database).GetOperation(ctx, id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed build reached executor: %v", err)
	}
}

func TestAbortedWaitingAdmissionRollsBackSelectionAndNativeJob(t *testing.T) {
	f := buildSetup(t)
	ctx := context.Background()
	tx, err := f.service.database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := f.metadata.CaptureSelection(ctx, tx, f.environment.Id, 2)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	intent, identity, err := operations.IntentIdentity(operations.Intent{RequestID: uuid.NewString(), Kind: operations.Deploy, EnvironmentID: f.environment.Id, ExpectedVersion: 2, SourceSHA: fixtureSHA})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := f.builds.insert(ctx, tx, intent, identity, selection, 0, 0, 0); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	waiting, err := store.New(tx).GetWaitingBuild(ctx, intent.RequestID)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.New(f.service.database).GetWaitingBuild(ctx, intent.RequestID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("aborted request persisted: %v", err)
	}
	writer, err := river.NewClient(riversqlite.New(f.service.database), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.JobGet(ctx, waiting.RiverJobID); !errors.Is(err, rivertype.ErrNotFound) {
		t.Fatalf("aborted native job persisted: %v", err)
	}
}
func TestFreshExplicitIntentRecoversExhaustedBuildWaitAndReusesKnownDispatch(t *testing.T) {
	f := buildSetup(t)
	ctx := context.Background()
	f.lostDispatch = true
	oldID := f.request(t)
	if _, err := f.builds.PollBuild(ctx, oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.database.ExecContext(ctx, "UPDATE waiting_builds SET failures=9 WHERE id=?", oldID); err != nil {
		t.Fatal(err)
	}
	waiting, err := store.New(f.service.database).GetWaitingBuild(ctx, oldID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.builds.retry(ctx, waiting, &Failure{}); err != nil {
		t.Fatal(err)
	}
	waiting, err = store.New(f.service.database).GetWaitingBuild(ctx, oldID)
	if err != nil || waiting.State != "needs_attention" {
		t.Fatalf("bounded polling did not expose exhaustion: %v", err)
	}
	id := f.request(t)
	if id == oldID {
		t.Fatal("new explicit intent reused exhausted identity")
	}
	waiting, err = store.New(f.service.database).GetWaitingBuild(ctx, oldID)
	if err != nil || waiting.State != "superseded" {
		t.Fatalf("explicit intent could not recover build-only uncertainty: %v", err)
	}
	if _, err := f.builds.PollBuild(ctx, id); err != nil {
		t.Fatal(err)
	}
	f.due(t, id)
	if _, err := f.builds.PollBuild(ctx, id); err != nil {
		t.Fatal(err)
	}
	current, err := store.New(f.service.database).GetWaitingBuild(ctx, id)
	if err != nil || current.State != "queued" {
		t.Fatalf("verified prior manual run not reused: %v", err)
	}
	if f.dispatches.Load() != 1 {
		t.Fatal("fresh intent rebuilt an existing exact successful dispatch")
	}
}
