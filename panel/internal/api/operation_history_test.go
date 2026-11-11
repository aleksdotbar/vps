package api_test

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/gen/go/panel/v1/panelv1connect"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

func TestOperationHistoryPreservesBuildIdentityAndScopedPagination(t *testing.T) {
	ctx := context.Background()
	installation, _, environment := rpcBuildSetup(t, projectProvider(t))
	client := panelv1connect.NewPanelServiceClient(installation.client, installation.origin)
	requestID := "00000000-0000-4000-8000-000000000003"
	request := connect.NewRequest(&panelv1.DeployEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: requestID}, EnvironmentId: environment.Id, ExpectedVersion: environment.Version, SourceSha: "0123456789abcdef0123456789abcdef01234567"})
	request.Header().Set("Origin", installation.origin)
	if _, err := client.DeployEnvironment(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := installation.database.ExecContext(ctx, "UPDATE waiting_builds SET created_at=1000,updated_at=1000 WHERE id=?", requestID); err != nil {
		t.Fatal(err)
	}

	insert := func(id, env, server string, created int64) {
		t.Helper()
		err := store.New(installation.database).CreateOperation(ctx, store.CreateOperationParams{ID: id, IntentJson: []byte(`{"nonpublic":"fixture-private-intent"}`), Fingerprint: make([]byte, 32), Kind: "deploy", ServerID: server, EnvironmentID: env, ReleaseID: uuid.NewString(), BindingVersion: 1, CreatedAt: created, UpdatedAt: created})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := installation.database.ExecContext(ctx, "UPDATE operations SET state='failed',outcome='failure',reason='confirmed_failure' WHERE id=?", id); err != nil {
			t.Fatal(err)
		}
	}
	secondID, thirdID := "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000001"
	otherEnvironment, otherServer := uuid.NewString(), uuid.NewString()
	insert(secondID, environment.Id, environment.Settings.ServerId, 1000)
	insert(thirdID, otherEnvironment, otherServer, 999)
	page := &panelv1.ListOperationsRequest{Page: &panelv1.PageRequest{PageSize: 1}}
	anonymousHTTP := *installation.server.Client()
	anonymousHTTP.Jar = nil
	anonymous := panelv1connect.NewPanelServiceClient(&anonymousHTTP, installation.origin)
	if _, err := anonymous.ListOperations(ctx, connect.NewRequest(page)); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("history exposed without session: %v", err)
	}
	first, err := client.ListOperations(ctx, connect.NewRequest(page))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Msg.Operations) != 1 || first.Msg.Operations[0].Id != requestID || first.Msg.Operations[0].State != panelv1.OperationState_OPERATION_STATE_WAITING_FOR_BUILD || first.Msg.Page.NextPageToken == "" {
		t.Fatalf("incorrect first page: %v", first.Msg)
	}

	// Promotion must replace the same history entry without moving its cursor.
	insert(requestID, environment.Id, environment.Settings.ServerId, 2000)
	if _, err := installation.database.ExecContext(ctx, "UPDATE operations SET state='recovering',outcome='partial',reason='partial_application' WHERE id=?", requestID); err != nil {
		t.Fatal(err)
	}
	page.Page.PageToken, page.Page.PageSize = first.Msg.Page.NextPageToken, 50
	rest, err := client.ListOperations(ctx, connect.NewRequest(page))
	if err != nil {
		t.Fatal(err)
	}
	if len(rest.Msg.Operations) != 2 || rest.Msg.Operations[0].Id != secondID || rest.Msg.Operations[1].Id != thirdID || rest.Msg.Page.NextPageToken != "" {
		t.Fatalf("promotion skipped/duplicated history: %v", rest.Msg)
	}
	detail, err := client.GetOperation(ctx, connect.NewRequest(&panelv1.GetOperationRequest{Id: requestID}))
	if err != nil {
		t.Fatal(err)
	}
	if detail.Msg.Operation.State != panelv1.OperationState_OPERATION_STATE_RECOVERING || detail.Msg.Operation.Outcome != panelv1.OperationOutcome_OPERATION_OUTCOME_PARTIAL || detail.Msg.Operation.CreatedAt.AsTime().UnixMilli() != 1000 {
		t.Fatalf("detail lost admission or partial outcome: %v", detail.Msg)
	}
	if _, err := installation.database.ExecContext(ctx, "UPDATE operations SET build_request_id=NULL WHERE id=?", requestID); err == nil {
		t.Fatal("promotion association was mutable")
	}
	_, err = installation.database.ExecContext(ctx, `INSERT INTO waiting_builds(id,environment_id,server_id,intent_json,fingerprint,selection_json,binding_version,automatic,state,next_poll_at,created_at,updated_at,expires_at)
SELECT ?,environment_id,server_id,intent_json,fingerprint,selection_json,binding_version,automatic,'failed',next_poll_at,created_at,updated_at,expires_at FROM waiting_builds WHERE id=?`, secondID, requestID)
	if err == nil || !strings.Contains(err.Error(), "build request must precede operation") {
		t.Fatalf("standalone history identity accepted later waiting record: %v", err)
	}

	for _, test := range []struct {
		name        string
		env, server *string
		want        int
	}{
		{"all", nil, nil, 3},
		{"environment", &environment.Id, nil, 2},
		{"server", nil, &otherServer, 1},
		{"both", &environment.Id, &environment.Settings.ServerId, 2},
		{"mismatched", &environment.Id, &otherServer, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, err := client.ListOperations(ctx, connect.NewRequest(&panelv1.ListOperationsRequest{EnvironmentId: test.env, ServerId: test.server, Page: &panelv1.PageRequest{PageSize: 50}}))
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Msg.Operations) != test.want {
				t.Fatalf("got %d operations, want %d", len(response.Msg.Operations), test.want)
			}
			if strings.Contains(response.Msg.String(), "fixture-private-intent") {
				t.Fatal("history exposed raw operation intent")
			}
		})
	}
	page.EnvironmentId = &environment.Id
	if _, err := client.ListOperations(ctx, connect.NewRequest(page)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("history cursor crossed filter scope: %v", err)
	}
}
