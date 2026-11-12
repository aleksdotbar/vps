package githubflow

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riversqlite"
	"github.com/riverqueue/river/rivertype"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/compose"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/projects"
	"github.com/aleksdotbar/vps/panel/internal/registry"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

const (
	fixtureSHA = "0123456789abcdef0123456789abcdef01234567"
	appImage   = "ghcr.io/example/stateless-app"
	workImage  = "ghcr.io/example/stateless-worker"
)

// fakeImages answers digests per repository; absent repositories are not pushed yet.
type fakeImages struct {
	mu      sync.Mutex
	digests map[string]string
	err     error
	calls   atomic.Int64
}

func (f *fakeImages) Resolve(_ context.Context, repository, tag string) (string, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	digest, exists := f.digests[repository+":"+tag]
	if !exists {
		return "", registry.ErrNotFound
	}
	return digest, nil
}
func (f *fakeImages) push(repository, tag, digest string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.digests[repository+":"+tag] = digest
}

type buildFixture struct {
	service     *Service
	metadata    *projects.Service
	builds      *Builds
	runtime     *operations.Runtime
	environment *panelv1.Environment
	files       map[string][]byte
	images      *fakeImages
	head        atomic.Value
	tokenMints  atomic.Int64
	sourceReads atomic.Int64
	branchReads atomic.Int64
	failBranch  atomic.Bool
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
	project, err := metadata.CreateProject(ctx, &panelv1.CreateProjectRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Name: "generic", Repository: &panelv1.Repository{GithubRepositoryId: 1, Owner: "operator", Name: "generic"}})
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
	fixture := &buildFixture{service: service, metadata: metadata, environment: saved.Environment, files: make(map[string][]byte), images: &fakeImages{digests: make(map[string]string)}}
	fixture.head.Store(fixtureSHA)
	root, err := os.OpenRoot("../../../examples/stateless")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	for _, name := range []string{"compose.yaml", "compose.staging.yaml"} {
		data, err := root.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		fixture.files[name] = data
	}
	service.transport = fixture.respond(t)
	// Native Compose is only required by tests that queue releases.
	command := os.Getenv("PANEL_TEST_COMPOSE")
	if command == "" {
		command = "docker-compose"
	}
	resolver, _ := compose.New(command)
	runtime, err := operations.New(ctx, service.database, nil, func(workers *river.Workers, runtime *operations.Runtime) ([]*river.PeriodicJob, error) {
		fixture.builds = NewBuilds(service, metadata, resolver, runtime, fixture.images)
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
func requireCompose(t *testing.T, f *buildFixture) {
	t.Helper()
	if f.builds.resolver == nil {
		t.Fatal(compose.ErrUnavailable)
	}
}
func (f *buildFixture) respond(t *testing.T) http.RoundTripper {
	return fixtureTransport(func(request *http.Request) (*http.Response, error) {
		var value any
		switch path := request.URL.Path; {
		case path == "/app/installations/10/access_tokens":
			f.tokenMints.Add(1)
			var body struct {
				Permissions map[string]string `json:"permissions"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || len(body.Permissions) != 1 || body.Permissions["contents"] != "read" {
				t.Fatalf("installation token requested more than contents read: %v", body.Permissions)
			}
			value = map[string]any{"token": uuid.NewString(), "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
		case strings.HasPrefix(path, "/repos/operator/generic/branches/"):
			f.branchReads.Add(1)
			if f.failBranch.Load() {
				return nil, errors.New("fixture provider unavailable")
			}
			value = map[string]any{"name": strings.TrimPrefix(path, "/repos/operator/generic/branches/"), "commit": map[string]any{"sha": f.head.Load().(string)}}
		case strings.HasPrefix(path, "/repos/operator/generic/contents/"):
			f.sourceReads.Add(1)
			if !sourceSHA(request.URL.Query().Get("ref")) {
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
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data)), Request: request}, nil
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
	if _, err := f.service.database.ExecContext(context.Background(), "UPDATE waiting_builds SET next_poll_at=0 WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
}
func digest(fill string) string { return "sha256:" + strings.Repeat(fill, 64) }

func TestMissingImageTagWaitsThenFailsNamingTheImage(t *testing.T) {
	f := buildSetup(t)
	ctx := context.Background()
	id := f.request(t)
	if delay, err := f.builds.PollBuild(ctx, id); err != nil || delay != pollInterval {
		t.Fatalf("missing image did not keep waiting: %v %v", delay, err)
	}
	waiting, err := store.New(f.service.database).GetWaitingBuild(ctx, id)
	if err != nil || waiting.State != "waiting" || waiting.Reason != waitingForImage+appImage+":"+fixtureSHA || waiting.Failures != 0 {
		t.Fatalf("waiting reason does not name the missing image: %+v %v", waiting, err)
	}
	// One pushed image is not enough: every service tag must exist.
	f.images.push(appImage, fixtureSHA, digest("1"))
	f.due(t, id)
	if _, err := f.builds.PollBuild(ctx, id); err != nil {
		t.Fatal(err)
	}
	waiting, err = store.New(f.service.database).GetWaitingBuild(ctx, id)
	if err != nil || waiting.Reason != waitingForImage+workImage+":"+fixtureSHA {
		t.Fatalf("second service image not awaited: %+v %v", waiting, err)
	}
	if _, err := f.service.database.ExecContext(ctx, "UPDATE waiting_builds SET expires_at=? WHERE id=?", time.Now().Add(-time.Second).UnixMilli(), id); err != nil {
		t.Fatal(err)
	}
	if delay, err := f.builds.PollBuild(ctx, id); err != nil || delay != 0 {
		t.Fatalf("expired wait kept snoozing: %v", err)
	}
	waiting, err = store.New(f.service.database).GetWaitingBuild(ctx, id)
	if err != nil || waiting.State != "failed" || waiting.Reason != imageNotFound+workImage+":"+fixtureSHA {
		t.Fatalf("expired wait lost its image reference: %+v %v", waiting, err)
	}
}
func TestIncompatibleImageFailsWithoutWaiting(t *testing.T) {
	f := buildSetup(t)
	ctx := context.Background()
	f.images.err = registry.ErrIncompatible
	id := f.request(t)
	if delay, err := f.builds.PollBuild(ctx, id); err != nil || delay != 0 {
		t.Fatalf("incompatible image entered backoff: %v %v", delay, err)
	}
	waiting, err := store.New(f.service.database).GetWaitingBuild(ctx, id)
	if err != nil || waiting.State != "failed" || waiting.Reason != "incompatible_image" {
		t.Fatalf("incompatible image not terminal: %+v %v", waiting, err)
	}
}
func TestUnavailableRegistryBacksOffAndKeepsWaitingReason(t *testing.T) {
	f := buildSetup(t)
	ctx := context.Background()
	id := f.request(t)
	if _, err := f.builds.PollBuild(ctx, id); err != nil {
		t.Fatal(err)
	}
	f.images.err = registry.ErrUnavailable
	f.due(t, id)
	if delay, err := f.builds.PollBuild(ctx, id); err != nil || delay <= pollInterval {
		t.Fatalf("registry failure not backed off: %v %v", delay, err)
	}
	waiting, err := store.New(f.service.database).GetWaitingBuild(ctx, id)
	if err != nil || waiting.State != "waiting" || waiting.Failures != 1 || !strings.HasPrefix(waiting.Reason, waitingForImage) {
		t.Fatalf("transient registry failure lost wait state: %+v %v", waiting, err)
	}
}
func TestResolvedImagesPromoteImmutableReleaseAndReplayWithoutProvider(t *testing.T) {
	f := buildSetup(t)
	requireCompose(t, f)
	ctx := context.Background()
	f.images.push(appImage, fixtureSHA, digest("1"))
	f.images.push(workImage, fixtureSHA, digest("2"))
	id := f.request(t)
	if _, err := f.metadata.SaveSecrets(ctx, &panelv1.SaveEnvironmentSecretsRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, EnvironmentId: f.environment.Id, ExpectedVersion: f.environment.Version, Upserts: []*panelv1.SecretValue{{Name: "API_TOKEN", Value: "later-pending-private-api"}}}); err != nil {
		t.Fatal(err)
	}
	if delay, err := f.builds.PollBuild(ctx, id); err != nil || delay != 0 {
		t.Fatalf("resolved images not promoted: %v", err)
	}
	operation, err := store.New(f.service.database).GetOperation(ctx, id)
	if err != nil || operation.ReleaseID == "" {
		t.Fatalf("stable operation missing: %v", err)
	}
	release, err := store.New(f.service.database).GetRelease(ctx, operation.ReleaseID)
	if err != nil || release.SecretVersion != 2 || release.SourceSha != fixtureSHA {
		t.Fatalf("captured selection changed: %v", err)
	}
	for _, pinned := range []string{appImage + "@" + digest("1"), workImage + "@" + digest("2")} {
		if !bytes.Contains(release.ComposeConfig, []byte(pinned)) {
			t.Fatalf("release does not pin %s", pinned)
		}
	}
	if bytes.Contains(release.ComposeConfig, []byte(":"+fixtureSHA)) || bytes.Contains(release.ComposeConfig, []byte("captured-private")) || bytes.Contains(release.ComposeConfig, []byte("later-pending")) {
		t.Fatal("release kept a moving tag or plaintext")
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
func TestSharedRepositoryResolvesOnce(t *testing.T) {
	f := buildSetup(t)
	f.files["compose.yaml"] = bytes.ReplaceAll(f.files["compose.yaml"], []byte(workImage), []byte(appImage))
	f.images.push(appImage, fixtureSHA, digest("3"))
	tree := t.TempDir()
	for name, data := range f.files {
		if err := os.WriteFile(tree+"/"+name, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	artifact, missing, err := f.builds.resolveImages(context.Background(), tree, "compose.staging.yaml", fixtureSHA)
	if err != nil || missing != "" || f.images.calls.Load() != 1 {
		t.Fatalf("shared repository resolved repeatedly: %v %q %d", err, missing, f.images.calls.Load())
	}
	if artifact.Services["app"].Image != appImage+"@"+digest("3") || artifact.Services["worker"].Image != appImage+"@"+digest("3") || artifact.SourceSHA != fixtureSHA {
		t.Fatalf("unexpected digest set: %+v", artifact)
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
	if err := f.builds.insert(ctx, tx, intent, identity, selection); err != nil {
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
func TestExpiredReasonNamesAwaitedImage(t *testing.T) {
	if got := expiredReason(waitingForImage + appImage + ":" + fixtureSHA); got != imageNotFound+appImage+":"+fixtureSHA {
		t.Fatalf("expired reason %q", got)
	}
	if got := expiredReason(""); got != "build_timeout" {
		t.Fatalf("expired reason without image %q", got)
	}
}
