package api_test

import (
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/gen/go/panel/v1/panelv1connect"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

func TestRollbackRPCRequiresAuthenticationAndOriginAndReturnsQueuedReplay(t *testing.T) {
	var calls atomic.Int64
	var unavailable atomic.Bool
	base := projectProvider(t)
	provider := projectTransport(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if unavailable.Load() {
			return nil, errors.New("fixture unavailable")
		}
		return base.RoundTrip(request)
	})
	installation, _, environment := rpcBuildSetup(t, provider)
	ctx := t.Context()
	release := uuid.NewString()
	queries := store.New(installation.database)
	if err := queries.CreateRelease(ctx, store.CreateReleaseParams{ID: release, EnvironmentID: environment.Id, ServerID: environment.Settings.ServerId, RepositoryID: 1, Branch: "old", SourceSha: strings.Repeat("a", 40), EnvironmentVersion: 1, BindingVersion: 1, ImagesJson: []byte(`[]`), DomainsJson: []byte(`[]`), SecretVersion: 1, ComposeConfig: []byte(`{"services":{"app":{}}}`), SecretBindingsJson: []byte(`[]`), ConfigSha256: strings.Repeat("a", 64), CreatedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := queries.CreateReleaseService(ctx, store.CreateReleaseServiceParams{ReleaseID: release, Name: "app", ImageDigest: "ghcr.io/example/app@sha256:" + strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	request := connect.NewRequest(&panelv1.RollbackEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, EnvironmentId: environment.Id, ExpectedVersion: environment.Version, ReleaseId: release})
	request.Header().Set("Origin", installation.origin)
	anonymousHTTP := *installation.client
	anonymousHTTP.Jar = nil
	anonymous := panelv1connect.NewPanelServiceClient(&anonymousHTTP, installation.origin)
	before := calls.Load()
	if _, err := anonymous.RollbackEnvironment(ctx, request); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated rollback returned %s: %v", connect.CodeOf(err), err)
	}
	client := panelv1connect.NewPanelServiceClient(installation.client, installation.origin)
	request.Header().Set("Origin", "https://foreign.example")
	if _, err := client.RollbackEnvironment(ctx, request); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("foreign origin returned %s: %v", connect.CodeOf(err), err)
	}
	if calls.Load() != before {
		t.Fatal("rejected authentication/origin contacted provider")
	}
	request.Header().Set("Origin", installation.origin)
	first, err := client.RollbackEnvironment(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Msg.Operation.Id != request.Msg.Mutation.RequestId || first.Msg.Operation.Kind != panelv1.OperationKind_OPERATION_KIND_ROLLBACK || first.Msg.Operation.State != panelv1.OperationState_OPERATION_STATE_QUEUED || first.Msg.Operation.Outcome != panelv1.OperationOutcome_OPERATION_OUTCOME_PENDING {
		t.Fatalf("rollback claimed execution or lost identity: %+v", first.Msg.Operation)
	}
	unavailable.Store(true)
	before = calls.Load()
	if replay, err := client.RollbackEnvironment(ctx, request); err != nil || replay.Msg.Operation.Id != first.Msg.Operation.Id || calls.Load() != before {
		t.Fatalf("replay required provider: %v", err)
	}
	request.Msg.ReleaseId = uuid.NewString()
	if _, err := client.RollbackEnvironment(ctx, request); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("changed rollback identity mapped to %s: %v", connect.CodeOf(err), err)
	}
}
