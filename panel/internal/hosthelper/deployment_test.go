package hosthelper

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aleksdotbar/vps/panel/internal/deployment"
)

func TestDeploymentStagingRequiresSettledMatchingHostAuthority(t *testing.T) {
	request := deployment.StageRequest{SchemaVersion: 1, Action: "deploy_stage", Challenge: strings.Repeat("a", 64), Manifest: deployment.Manifest{Identity: deployment.Identity{InstallationID: "11111111-1111-4111-8111-111111111111", ServerID: "22222222-2222-4222-8222-222222222222", EnvironmentID: "33333333-3333-4333-8333-333333333333", OperationID: "44444444-4444-4444-8444-444444444444", ReleaseID: "55555555-5555-4555-8555-555555555555", BindingVersion: 1, ExecutionDeadline: 1}}}
	// #nosec G117 -- Empty synthetic secret map; this checks authorization before any state writes or native commands.
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatchHelper(t.Context(), data, nil); !errors.Is(err, ErrIdentity) {
		t.Fatal("stage bypassed settled-final lease")
	}
	lease := HostLease{InstallationID: request.Manifest.Identity.InstallationID, ServerID: request.Manifest.Identity.ServerID, System: "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-nixos-system-fixture", Release: func() error { return nil }}
	if !deploymentLeaseMatches(request.Manifest.Identity, lease) {
		t.Fatal("matching identity rejected")
	}
	lease.ServerID = lease.InstallationID
	if deploymentLeaseMatches(request.Manifest.Identity, lease) {
		t.Fatal("foreign server lease accepted")
	}
}
