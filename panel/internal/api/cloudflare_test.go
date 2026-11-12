package api_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/gen/go/panel/v1/panelv1connect"
)

type cloudflareTransport func(*http.Request) (*http.Response, error)

func (f cloudflareTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestCloudflareRPCProtectsCredentialsAndRequiresAuthenticatedOrigin(t *testing.T) {
	calls := 0
	i := setupProvider(t, cloudflareTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"errors":[{"message":"fixture-private-token"}]}`)), Request: request}, nil
	}))
	ctx := context.Background()
	client := panelv1connect.NewCloudflareServiceClient(i.client, i.origin)
	input := &panelv1.ConnectAccountRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, AccountId: strings.Repeat("a", 32), Token: "fixture-private-token", ZoneIds: []string{strings.Repeat("b", 32)}}
	request := connect.NewRequest(input)
	request.Header().Set("Origin", i.origin)
	if _, err := client.ConnectAccount(ctx, request); connect.CodeOf(err) != connect.CodeUnauthenticated || calls != 0 {
		t.Fatalf("unauthenticated connection reached provider: calls=%d error=%v", calls, err)
	}
	if _, err := login(i, "initial-private-password", i.origin); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ConnectAccount(ctx, connect.NewRequest(input)); connect.CodeOf(err) != connect.CodePermissionDenied || calls != 0 {
		t.Fatalf("connection without origin reached provider: calls=%d error=%v", calls, err)
	}
	status, err := client.GetCloudflareIntegration(ctx, connect.NewRequest(&panelv1.GetCloudflareIntegrationRequest{}))
	if err != nil || status.Msg.GetIntegration().GetConfigured() {
		t.Fatalf("initial metadata read failed: %v", err)
	}
	_, err = client.ConnectAccount(ctx, request)
	var failure *connect.Error
	// A definite 401 is actionable guidance, never the provider's own text.
	if !errors.As(err, &failure) || failure.Code() != connect.CodePermissionDenied || !strings.Contains(failure.Message(), "account-owned API token") || strings.Contains(err.Error(), input.Token) || calls != 1 {
		t.Fatalf("provider error leaked credentials or retried: calls=%d error=%v", calls, err)
	}
	input.ZoneIds = append(input.ZoneIds, input.ZoneIds[0])
	if _, err := client.ConnectAccount(ctx, request); connect.CodeOf(err) != connect.CodeInvalidArgument || calls != 1 {
		t.Fatalf("duplicate zones reached provider: calls=%d error=%v", calls, err)
	}
}
