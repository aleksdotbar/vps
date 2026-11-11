package api_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/gen/go/panel/v1/panelv1connect"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

func TestAuthenticatedMetadataEditsKeepSecretsPendingAndReplaySafely(t *testing.T) {
	installation := setupProvider(t, projectProvider(t))
	seedProjectIntegration(t, installation)
	ctx := context.Background()
	serverID := uuid.NewString()
	if err := store.New(installation.database).CreateServer(ctx, store.CreateServerParams{ID: serverID, Name: "generic", TailscaleAddress: "generic.example.ts.net", SshFingerprint: "independently-verified-fixture", CreatedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	client := panelv1connect.NewPanelServiceClient(installation.client, installation.origin)
	projectRequest := connect.NewRequest(&panelv1.CreateProjectRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Name: "generic", Repository: &panelv1.Repository{GithubRepositoryId: 1, Owner: "example", Name: "generic"}, GithubWorkflowId: 2})
	projectRequest.Header().Set("Origin", installation.origin)
	if _, err := client.CreateProject(ctx, projectRequest); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated metadata mutation accepted: %v", err)
	}
	if _, err := login(installation, "initial-private-password", installation.origin); err != nil {
		t.Fatal(err)
	}
	project, err := client.CreateProject(ctx, projectRequest)
	if err != nil {
		t.Fatal(err)
	}
	environmentRequest := connect.NewRequest(&panelv1.CreateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, ProjectId: project.Msg.Project.Id, Settings: &panelv1.EnvironmentSettings{Name: "staging", ServerId: serverID, Branch: "main", ComposeOverridePath: "compose.staging.yaml"}})
	environmentRequest.Header().Set("Origin", installation.origin)
	environment, err := client.CreateEnvironment(ctx, environmentRequest)
	if err != nil {
		t.Fatal(err)
	}
	save := connect.NewRequest(&panelv1.SaveEnvironmentSecretsRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, EnvironmentId: environment.Msg.Environment.Id, ExpectedVersion: 1, Upserts: []*panelv1.SecretValue{{Name: "API_TOKEN", Value: "api-private-value"}}})
	save.Header().Set("Origin", installation.origin)
	result, err := client.SaveEnvironmentSecrets(ctx, save)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := client.SaveEnvironmentSecrets(ctx, save)
	if err != nil || !proto.Equal(result.Msg, replay.Msg) {
		t.Fatalf("RPC replay changed result: %v", err)
	}
	if !result.Msg.Environment.PendingChanges || result.Msg.Environment.CurrentReleaseId != "" || result.Msg.Environment.Version != 2 || len(result.Msg.Environment.SecretNames) != 1 {
		t.Fatal("secret edit changed deployed state")
	}
	duplicates := connect.NewRequest(&panelv1.SaveEnvironmentSecretsRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, EnvironmentId: environment.Msg.Environment.Id, ExpectedVersion: 2, Upserts: []*panelv1.SecretValue{{Name: "API_TOKEN", Value: "first"}, {Name: "API_TOKEN", Value: "second"}}})
	duplicates.Header().Set("Origin", installation.origin)
	if _, err := client.SaveEnvironmentSecrets(ctx, duplicates); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("duplicate secret input accepted: %v", err)
	}
	listing, err := client.ListEnvironments(ctx, connect.NewRequest(&panelv1.ListEnvironmentsRequest{ProjectId: project.Msg.Project.Id, Page: &panelv1.PageRequest{PageSize: 1}}))
	if err != nil || len(listing.Msg.Environments) != 1 {
		t.Fatalf("environment list failed: %v", err)
	}
}

type projectTransport func(*http.Request) (*http.Response, error)

func (f projectTransport) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
func projectProvider(t *testing.T) http.RoundTripper {
	t.Helper()
	return projectTransport(func(request *http.Request) (*http.Response, error) {
		var value string
		switch request.URL.Path {
		case "/app/installations/10/access_tokens":
			value = `{"token":"ephemeral-fixture"}`
		case "/repositories/1":
			value = `{"id":1,"name":"generic","owner":{"id":1,"login":"example"}}`
		case "/repos/example/generic/actions/workflows/2/runs":
			value = `{"total_count":0,"workflow_runs":[]}`
		case "/repos/example/generic/branches/main":
			value = `{"name":"main","commit":{"sha":"0123456789abcdef0123456789abcdef01234567"}}`
		case "/repos/example/generic/actions/workflows/2":
			value = `{"id":2,"path":".github/workflows/panel-build.yaml","state":"active"}`
		default:
			t.Fatalf("unexpected provider call %s", request.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(value)), Request: request}, nil
	})
}
func seedProjectIntegration(t *testing.T, installation *installation) {
	t.Helper()
	ctx := context.Background()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(map[string]string{"client_id": "Iv1.generic", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := securestate.NewVault(installation.database, installation.cipher).SaveCredential(ctx, "github-app", 0, encoded)
	if err != nil {
		t.Fatal(err)
	}
	queries := store.New(installation.database)
	if err := queries.CreateGitHubIntegration(ctx, store.CreateGitHubIntegrationParams{AppID: 2, OwnerID: 1, OwnerLogin: "example", AppSlug: "generic", CredentialVersion: ref.Version}); err != nil {
		t.Fatal(err)
	}
	if err := queries.SetGitHubInstallation(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if err := queries.CreateGitHubRepository(ctx, store.CreateGitHubRepositoryParams{ID: 1, Owner: "example", Name: "generic"}); err != nil {
		t.Fatal(err)
	}
}
