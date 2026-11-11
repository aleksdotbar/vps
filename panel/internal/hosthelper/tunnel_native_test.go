package hosthelper

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestTunnelActionsRequireSettledFinalAuthority(t *testing.T) {
	request := tunnelRequestFixture()
	for _, action := range []string{"tunnel_prepare", "tunnel_status", "tunnel_finalize"} {
		request.Action = action
		if action == "tunnel_finalize" {
			request.TunnelID = request.OperationID
		}
		data, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := dispatchHelper(t.Context(), data, nil); !errors.Is(err, ErrIdentity) {
			t.Fatalf("action %s bypassed final authority: %v", action, err)
		}
	}
	for _, lease := range []FinalLease{{}, {InstallationID: request.InstallationID, ServerID: request.ServerID, FinalSystem: "/caller/path"}} {
		released := false
		lease.Release = func() error { released = true; return nil }
		if _, err := handleTunnel(t.Context(), request, func(_ context.Context) (FinalLease, error) { return lease, nil }); !errors.Is(err, ErrIdentity) || !released {
			t.Fatal("invalid final authority accepted or leaked exclusion")
		}
	}
}
