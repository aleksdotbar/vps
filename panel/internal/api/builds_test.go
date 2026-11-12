package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/gen/go/panel/v1/panelv1connect"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/projects"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

func rpcBuildSetup(t *testing.T, provider http.RoundTripper) (*installation, *projects.Service, *panelv1.Environment) {
	t.Helper()
	installation := setupConfigured(t, provider, true)
	seedProjectIntegration(t, installation)
	ctx := context.Background()
	metadata := projects.New(installation.database, installation.cipher)
	serverID := uuid.NewString()
	queries := store.New(installation.database)
	if err := queries.CreateServer(ctx, store.CreateServerParams{ID: serverID, Name: "generic", TailscaleAddress: "generic.example.ts.net", SshFingerprint: "independently-verified-fixture", CreatedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := queries.ObserveServer(ctx, store.ObserveServerParams{ID: serverID, Readiness: "ready", ObservedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	project, err := metadata.CreateProject(ctx, &panelv1.CreateProjectRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Name: "generic", Repository: &panelv1.Repository{GithubRepositoryId: 1, Owner: "example", Name: "generic"}})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := metadata.CreateEnvironment(ctx, &panelv1.CreateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, ProjectId: project.Project.Id, Settings: &panelv1.EnvironmentSettings{Name: "staging", ServerId: serverID, Branch: "main", ComposeOverridePath: "compose.staging.yaml"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := login(installation, "initial-private-password", installation.origin); err != nil {
		t.Fatal(err)
	}
	return installation, metadata, environment.Environment
}
func TestDeployRPCPersistsWaitingIdentityAndMapsReplayAndHostConflict(t *testing.T) {
	ctx := context.Background()
	base := projectProvider(t)
	var unavailable atomic.Bool
	provider := projectTransport(func(request *http.Request) (*http.Response, error) {
		if unavailable.Load() {
			return nil, errors.New("fixture unavailable")
		}
		return base.RoundTrip(request)
	})
	installation, _, environment := rpcBuildSetup(t, provider)
	client := panelv1connect.NewPanelServiceClient(installation.client, installation.origin)
	source, err := client.GetDeploymentSource(ctx, connect.NewRequest(&panelv1.GetDeploymentSourceRequest{EnvironmentId: environment.Id, ExpectedVersion: environment.Version}))
	if err != nil || source.Msg.SourceSha == "" {
		t.Fatalf("branch source unavailable: %v", err)
	}
	request := connect.NewRequest(&panelv1.DeployEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, EnvironmentId: environment.Id, ExpectedVersion: environment.Version, SourceSha: source.Msg.SourceSha})
	request.Header().Set("Origin", installation.origin)
	first, err := client.DeployEnvironment(ctx, request)
	if err != nil || first.Msg.Operation.Id != request.Msg.Mutation.RequestId || first.Msg.Operation.State != panelv1.OperationState_OPERATION_STATE_WAITING_FOR_BUILD {
		t.Fatalf("waiting request not returned: %v", err)
	}
	waiting, err := store.New(installation.database).GetWaitingBuild(ctx, first.Msg.Operation.Id)
	if err != nil || waiting.RiverJobID < 1 {
		t.Fatalf("intent/job not persisted: %v", err)
	}
	unavailable.Store(true)
	replay, err := client.DeployEnvironment(ctx, request)
	if err != nil || replay.Msg.Operation.Id != first.Msg.Operation.Id {
		t.Fatalf("replay required provider: %v", err)
	}
	request.Msg.SourceSha = strings.Repeat("a", 40)
	if _, err := client.DeployEnvironment(ctx, request); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("changed intent mapped to %s: %v", connect.CodeOf(err), err)
	}
	unavailable.Store(false)
	if err := store.New(installation.database).FinishWaitingBuild(ctx, store.FinishWaitingBuildParams{ID: waiting.ID, State: "failed", Reason: "build_failed", UpdatedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	tx, err := installation.database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = installation.runtime.Enqueue(ctx, tx, operations.Intent{RequestID: uuid.NewString(), Kind: operations.Enroll, Enrollment: &operations.EnrollmentInput{Name: "generic", Address: "generic.example.ts.net", Fingerprint: "independently-verified-fixture", User: "operator", BootstrapCredentialID: uuid.NewString()}}, operations.Assignment{ServerID: environment.Settings.ServerId})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	request.Msg.Mutation.RequestId = uuid.NewString()
	request.Msg.SourceSha = source.Msg.SourceSha
	if _, err := client.DeployEnvironment(ctx, request); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("host setup conflict mapped to %s: %v", connect.CodeOf(err), err)
	}
}
func TestDeployRPCCapacityRejectsWithoutOrphanIntentOrJob(t *testing.T) {
	ctx := context.Background()
	installation, metadata, environment := rpcBuildSetup(t, projectProvider(t))
	queries := store.New(installation.database)
	//128 is the supported unresolved-request limit, not a history/coverage load test.
	for i := 0; i < 128; i++ {
		created, err := metadata.CreateEnvironment(ctx, &panelv1.CreateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, ProjectId: environment.ProjectId, Settings: &panelv1.EnvironmentSettings{Name: fmt.Sprintf("pending-%d", i), ServerId: environment.Settings.ServerId, Branch: "main", ComposeOverridePath: "compose.staging.yaml"}})
		if err != nil {
			t.Fatal(err)
		}
		tx, err := installation.database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		selection, err := metadata.CaptureSelection(ctx, tx, created.Environment.Id, 1)
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		intent, identity, err := operations.IntentIdentity(operations.Intent{RequestID: uuid.NewString(), Kind: operations.Deploy, EnvironmentID: created.Environment.Id, ExpectedVersion: 1, SourceSHA: "0123456789abcdef0123456789abcdef01234567"})
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		intentData, err := json.Marshal(intent)
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		selectionData, err := json.Marshal(selection)
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		now := time.Now()
		if err := store.New(tx).CreateWaitingBuild(ctx, store.CreateWaitingBuildParams{ID: intent.RequestID, EnvironmentID: intent.EnvironmentID, ServerID: environment.Settings.ServerId, IntentJson: intentData, Fingerprint: identity, SelectionJson: selectionData, BindingVersion: 1, NextPollAt: now.UnixMilli(), CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli()}); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		job, err := installation.runtime.EnqueueBuild(ctx, tx, intent.RequestID)
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := store.New(tx).SetWaitingBuildJob(ctx, store.SetWaitingBuildJobParams{ID: intent.RequestID, RiverJobID: job}); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	request := connect.NewRequest(&panelv1.DeployEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, EnvironmentId: environment.Id, ExpectedVersion: 1, SourceSha: "0123456789abcdef0123456789abcdef01234567"})
	request.Header().Set("Origin", installation.origin)
	if _, err := panelv1connect.NewPanelServiceClient(installation.client, installation.origin).DeployEnvironment(ctx, request); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("capacity mapped to %s: %v", connect.CodeOf(err), err)
	}
	capacity, err := queries.WaitingBuildCapacity(ctx)
	if err != nil || capacity.Retained != 128 || capacity.Unresolved != 128 {
		t.Fatalf("capacity rejection left orphan intent: %v", err)
	}
}
