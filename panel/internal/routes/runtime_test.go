package routes

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func runtimeOperationFixture(t *testing.T, environment, hostname string) RuntimeOperation {
	t.Helper()
	operation := RuntimeOperation{InstallationID: "11111111-1111-4111-8111-111111111111", ManifestSHA256: strings.Repeat("a", 64), ExecutionDeadline: time.Now().Add(time.Minute).UnixMilli()}
	operation.Snapshot = Snapshot{SchemaVersion: 1, OperationID: uuid.NewString(), ReleaseID: uuid.NewString(), EnvironmentID: environment, ServerID: "22222222-2222-4222-8222-222222222222", InstallationID: operation.InstallationID, AccountID: strings.Repeat("1", 32), TunnelID: tunnel, Services: []string{"app"}, Bindings: []Binding{{ClaimID: uuid.NewString(), Hostname: hostname, ZoneID: strings.Repeat("2", 32), ZoneName: "example.com", CredentialVersion: 1, Service: "app", ContainerPort: 3000}}}
	var err error
	operation.Snapshot.SHA256, err = SnapshotSHA256(operation.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return operation
}

func runtimeRootsFixture(t *testing.T, operation RuntimeOperation) *runtimeRoots {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil { // #nosec G302 -- This is the private fixture directory, not a file; clear inherited special bits before exact native-mode checks.
		t.Fatal(err)
	}
	state, err := openRuntimeRoot(directory, 0700)
	if err != nil {
		t.Fatal(err)
	}
	connectorPath := filepath.Join(t.TempDir(), "connector")
	if err := os.Mkdir(connectorPath, 0755); err != nil { // #nosec G301 -- Fixture mirrors the native public-config directory; credential file remains protected 0600.
		t.Fatal(err)
	}
	connector, err := openRuntimeRoot(connectorPath, 0755)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := Compile(operation.Snapshot.TunnelID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &runtimeRoots{state: state, connector: connector, credentials: []byte("fixture credential bytes"), finalSystem: "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-nixos-system-missing", ledger: runtimeLedger{SchemaVersion: 1, InstallationID: operation.InstallationID, ServerID: operation.Snapshot.ServerID, AccountID: operation.Snapshot.AccountID, TunnelID: operation.Snapshot.TunnelID, ConfigSHA256: configurationHash(configuration), Environments: []runtimeEnvironment{}, Allocations: []runtimeAllocation{}}}
	t.Cleanup(r.close)
	if err := writeRuntimeFile(t.Context(), connector, "credentials.json", r.credentials, 0600, 4096); err != nil {
		t.Fatal(err)
	}
	if err := writeRuntimeFile(t.Context(), connector, "config.json", configuration, 0644, MaxConfigBytes); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRuntimeSnapshotRejectsChangedAndForeignImmutableInput(t *testing.T) {
	for _, scenario := range []string{"hash", "installation", "claim", "zone boundary", "service", "duplicate hostname"} {
		t.Run(scenario, func(t *testing.T) {
			operation := runtimeOperationFixture(t, environmentA, "app.example.com")
			switch scenario {
			case "hash":
				operation.Snapshot.SHA256 = strings.Repeat("b", 64)
			case "installation":
				operation.InstallationID = uuid.NewString()
			case "claim":
				operation.Snapshot.Bindings[0].ClaimID = "foreign"
			case "zone boundary":
				operation.Snapshot.Bindings[0].ZoneName = "ample.com"
			case "service":
				operation.Snapshot.Bindings[0].Service = "foreign"
			case "duplicate hostname":
				operation.Snapshot.Bindings = append(operation.Snapshot.Bindings, operation.Snapshot.Bindings[0])
			}
			if ValidateRuntimeOperation(operation) == nil {
				t.Fatal("changed route authority accepted")
			}
		})
	}
}

func TestRuntimeAllocationsPersistBeforeComposeAndReplayExactOrigins(t *testing.T) {
	operation := runtimeOperationFixture(t, environmentA, "app.example.com")
	r := runtimeRootsFixture(t, operation)
	prepared, err := prepareRuntime(t.Context(), r, operation)
	if err != nil || len(prepared.Origins) != 1 || prepared.Origins[0].LoopbackPort == 0 {
		t.Fatalf("prepare: %v %v", prepared, err)
	}
	var retained runtimeLedger
	if err := readRuntimeRecord(r.state, "ledger.json", &retained); err != nil || validateRuntimeLedger(retained) != nil || len(retained.Allocations) != 1 || !reflect.DeepEqual(retained.Allocations[0].Origins, prepared.Origins) {
		t.Fatalf("allocation not durable: %v", err)
	}
	r.ledger = retained
	replay, err := prepareRuntime(t.Context(), r, operation)
	if err != nil || !reflect.DeepEqual(prepared, replay) {
		t.Fatalf("replay changed ports: %v", err)
	}
	other := runtimeOperationFixture(t, environmentA, "app.example.com")
	if _, err := prepareRuntime(t.Context(), r, other); !errors.Is(err, ErrRuntimeBusy) {
		t.Fatalf("abandoned unresolved allocation: %v", err)
	}
	actual, err := readRuntimeFile(r.connector, "config.json", 0644, MaxConfigBytes)
	if err != nil || configurationHash(actual) != retained.ConfigSHA256 {
		t.Fatalf("allocation changed live configuration: %v", err)
	}
}

func TestRuntimeCASRejectsStaleSharedRevisionAndSameReleaseNewOwner(t *testing.T) {
	operation := runtimeOperationFixture(t, environmentA, "app.example.com")
	r := runtimeRootsFixture(t, operation)
	prepared, err := prepareRuntime(t.Context(), r, operation)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := planRuntimeApply(r.ledger, operation, prepared.Revision+1, prepared.ConfigSHA256); !errors.Is(err, ErrRuntimeConflict) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	if _, _, err := planRuntimeApply(r.ledger, operation, prepared.Revision, strings.Repeat("b", 64)); !errors.Is(err, ErrRuntimeConflict) {
		t.Fatalf("stale config accepted: %v", err)
	}
	foreign := contributionFor(operation, prepared.Origins)
	foreign.Key.OperationID = uuid.NewString()
	operation.ExpectedCurrentReleaseID = foreign.Key.ReleaseID
	r.ledger.Environments = []runtimeEnvironment{{EnvironmentID: environmentA, Current: &foreign}}
	configuration, err := runtimeConfiguration(r.ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.ledger.ConfigSHA256 = configurationHash(configuration)
	if _, _, err := planRuntimeApply(r.ledger, operation, r.ledger.Revision, r.ledger.ConfigSHA256); !errors.Is(err, ErrRuntimeConflict) {
		t.Fatalf("same release hid a new operation owner: %v", err)
	}
}

func TestRuntimeUnknownPublicationRemainsPendingAndRejectsForeignConfig(t *testing.T) {
	operation := runtimeOperationFixture(t, environmentA, "app.example.com")
	r := runtimeRootsFixture(t, operation)
	prepared, err := prepareRuntime(t.Context(), r, operation)
	if err != nil {
		t.Fatal(err)
	}
	pending, configuration, err := planRuntimeApply(r.ledger, operation, prepared.Revision, prepared.ConfigSHA256)
	if err != nil {
		t.Fatal(err)
	}
	r.ledger.Pending = &pending
	if err := saveRuntimeLedger(t.Context(), r.state, r.ledger); err != nil {
		t.Fatal(err)
	}
	receipt, err := advanceRuntimeMutation(t.Context(), r, operation, false)
	if err != nil || receipt.Phase != RuntimeApplying || receipt.ActiveReleaseID != "" {
		t.Fatalf("unpublished mutation reported ready: %v %v", receipt, err)
	}
	if _, err := ownedRuntimeConfiguration(r.ledger, operation.InstallationID, operation.Snapshot.ServerID, operation.Snapshot.AccountID, operation.Snapshot.TunnelID); !errors.Is(err, ErrTunnelRuntime) {
		t.Fatalf("pending config fell back to catch-all: %v", err)
	}
	// The fixture has no native systemctl; publication must remain unknown,
	// never claim that the new bytes were loaded by a running connector.
	if _, err := advanceRuntimeMutation(t.Context(), r, operation, true); !errors.Is(err, ErrTunnelRuntime) {
		t.Fatalf("missing native runtime accepted: %v", err)
	}
	actual, err := readRuntimeFile(r.connector, "config.json", 0644, MaxConfigBytes)
	if err != nil || !bytes.Equal(actual, configuration) || r.ledger.Pending == nil {
		t.Fatalf("interrupted publication lost intent: %v", err)
	}
	if err := r.connector.WriteFile("config.json", []byte("foreign"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := advanceRuntimeMutation(t.Context(), r, operation, true); !errors.Is(err, ErrRuntimeState) {
		t.Fatalf("foreign config overwritten: %v", err)
	}
}

func TestRuntimeRestorePreservesLatestOtherEnvironmentAndBothBaselines(t *testing.T) {
	operation := runtimeOperationFixture(t, environmentA, "app.example.com")
	r := runtimeRootsFixture(t, operation)
	old := runtimeOperationFixture(t, environmentA, "old.example.com")
	older := runtimeOperationFixture(t, environmentA, "older.example.com")
	other := runtimeOperationFixture(t, environmentB, "other.example.com")
	oldContribution := contributionFor(old, []Origin{{environmentA, "app", 3000, 23000}})
	olderContribution := contributionFor(older, []Origin{{environmentA, "app", 3000, 23001}})
	otherContribution := contributionFor(other, []Origin{{environmentB, "app", 3000, 23002}})
	r.ledger.Environments = []runtimeEnvironment{{EnvironmentID: environmentA, Current: &oldContribution, Previous: &olderContribution}, {EnvironmentID: environmentB, Current: &otherContribution}}
	r.ledger.Allocations = []runtimeAllocation{{Key: oldContribution.Key, Origins: oldContribution.Origins}, {Key: olderContribution.Key, Origins: olderContribution.Origins}, {Key: otherContribution.Key, Origins: otherContribution.Origins}}
	configuration, err := runtimeConfiguration(r.ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.ledger.ConfigSHA256 = configurationHash(configuration)
	operation.ExpectedCurrentReleaseID = old.Snapshot.ReleaseID
	prepared, err := prepareRuntime(t.Context(), r, operation)
	if err != nil {
		t.Fatal(err)
	}
	pending, _, err := planRuntimeApply(r.ledger, operation, prepared.Revision, prepared.ConfigSHA256)
	if err != nil {
		t.Fatal(err)
	}
	r.ledger.Pending = &pending
	// Exercise the durable ledger transition only. Native process and app
	// acceptance are independently required by production advance/runner calls.
	if _, err := confirmRuntimeMutation(t.Context(), r, operation); err != nil {
		t.Fatal(err)
	}
	newOther := runtimeOperationFixture(t, environmentB, "new-other.example.com")
	updatedOther := contributionFor(newOther, []Origin{{environmentB, "app", 3000, 23003}})
	r.ledger.Environments[1].Current = &updatedOther
	r.ledger.Environments[1].Previous = &otherContribution
	r.ledger.Allocations = append(r.ledger.Allocations, runtimeAllocation{Key: updatedOther.Key, Origins: updatedOther.Origins, PriorCurrent: &otherContribution})
	configuration, err = runtimeConfiguration(r.ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.ledger.Revision++
	r.ledger.ConfigSHA256 = configurationHash(configuration)
	restore := runtimePending{Key: operationKey(operation), Restore: true, FromRevision: r.ledger.Revision, FromConfigSHA256: r.ledger.ConfigSHA256, Target: r.ledger.Environments[0].Previous}
	configuration, err = runtimeConfiguration(r.ledger, &restore)
	if err != nil {
		t.Fatal(err)
	}
	restore.ConfigSHA256 = configurationHash(configuration)
	var compiled tunnelConfig
	if err := json.Unmarshal(configuration, &compiled); err != nil {
		t.Fatal(err)
	}
	hostnames := make(map[string]bool)
	for _, rule := range compiled.Ingress {
		hostnames[rule.Hostname] = true
	}
	if !hostnames["new-other.example.com"] || !hostnames["old.example.com"] || hostnames["other.example.com"] || hostnames["app.example.com"] {
		t.Fatal("restore replaced newer unrelated routes")
	}
	r.ledger.Pending = &restore
	receipt, err := confirmRuntimeMutation(t.Context(), r, operation)
	if err != nil || receipt.Phase != RuntimeRestored || receipt.ActiveReleaseID != old.Snapshot.ReleaseID || r.ledger.Environments[0].Current.Key != oldContribution.Key || r.ledger.Environments[0].Previous.Key != olderContribution.Key || r.ledger.Environments[1].Current.Key != updatedOther.Key {
		t.Fatalf("restore lost environment baselines: %v %v", receipt, err)
	}
	if validateRuntimeLedger(r.ledger) != nil {
		t.Fatal("restoration produced invalid durable state")
	}
	before, err := json.Marshal(r.ledger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepareRuntime(t.Context(), r, operation); !errors.Is(err, ErrRuntimeConflict) {
		t.Fatalf("restored candidate received fresh forward preparation: %v", err)
	}
	if _, _, err := planRuntimeApply(r.ledger, operation, r.ledger.Revision, r.ledger.ConfigSHA256); !errors.Is(err, ErrRuntimeConflict) {
		t.Fatalf("restored candidate planned forward application: %v", err)
	}
	after, err := json.Marshal(r.ledger)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("refused restored replay changed retained routes: %v", err)
	}
}

func TestRuntimeNoLedgerObservationCarriesNoReleaseAuthority(t *testing.T) {
	operation := runtimeOperationFixture(t, environmentA, "app.example.com")
	operation.Snapshot.Bindings = []Binding{}
	operation.Snapshot.InstallationID, operation.Snapshot.AccountID, operation.Snapshot.TunnelID = "", "", ""
	operation.ExpectedCurrentReleaseID = uuid.NewString()
	var err error
	operation.Snapshot.SHA256, err = SnapshotSHA256(operation.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	// No connector or state root is needed for a route-neutral observation.
	// The runner owns matching candidate/previous app and terminal-phase proof.
	receipt, err := observeRuntime(t.Context(), &runtimeRoots{}, operation)
	if err != nil || receipt.Phase != RuntimeNeutral || receipt.ActiveReleaseID != "" || receipt.Revision != 0 || receipt.ConfigSHA256 != "" || receipt.OperationID != operation.Snapshot.OperationID || receipt.RouteSnapshotSHA256 != operation.Snapshot.SHA256 {
		t.Fatalf("neutral route observation overstated authority: %v %v", receipt, err)
	}
}

func TestRuntimeProtectedPublicationRejectsSymlinkAndCorruptLedger(t *testing.T) {
	operation := runtimeOperationFixture(t, environmentA, "app.example.com")
	r := runtimeRootsFixture(t, operation)
	if err := saveRuntimeLedger(t.Context(), r.state, r.ledger); err != nil {
		t.Fatal(err)
	}
	if err := r.state.Symlink("ledger.json", "ledger.json.next"); err != nil {
		t.Fatal(err)
	}
	if err := saveRuntimeLedger(t.Context(), r.state, r.ledger); !errors.Is(err, ErrRuntimeState) {
		t.Fatalf("staging symlink accepted: %v", err)
	}
	if err := r.state.Remove("ledger.json.next"); err != nil {
		t.Fatal(err)
	}
	if err := r.state.WriteFile("ledger.json.next", []byte("uncommitted staging"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveRuntimeLedger(t.Context(), r.state, r.ledger); err != nil {
		t.Fatalf("interrupted staging did not recover: %v", err)
	}
	data, err := json.Marshal(r.ledger)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data[:len(data)-1], []byte(`,"foreign":true}`)...)
	if err := r.state.WriteFile("ledger.json", data, 0600); err != nil {
		t.Fatal(err)
	}
	var ledger runtimeLedger
	if err := readRuntimeRecord(r.state, "ledger.json", &ledger); !errors.Is(err, ErrRuntimeState) {
		t.Fatalf("unknown ledger fields accepted: %v", err)
	}
}

func TestRuntimeCancellationReleasesOnlyUnusedOwnAllocation(t *testing.T) {
	operation := runtimeOperationFixture(t, environmentA, "app.example.com")
	r := runtimeRootsFixture(t, operation)
	if _, err := prepareRuntime(t.Context(), r, operation); err != nil {
		t.Fatal(err)
	}
	if err := cancelRuntimePreparation(t.Context(), r, operation); err != nil || len(r.ledger.Allocations) != 0 {
		t.Fatalf("unused allocation not cancelled: %v", err)
	}
	if err := cancelRuntimePreparation(t.Context(), r, operation); err != nil {
		t.Fatalf("absent cancellation not idempotent: %v", err)
	}
	prepared, err := prepareRuntime(t.Context(), r, operation)
	if err != nil {
		t.Fatal(err)
	}
	pending, _, err := planRuntimeApply(r.ledger, operation, prepared.Revision, prepared.ConfigSHA256)
	if err != nil {
		t.Fatal(err)
	}
	r.ledger.Pending = &pending
	if err := cancelRuntimePreparation(t.Context(), r, operation); !errors.Is(err, ErrRuntimeBusy) {
		t.Fatalf("pending mutation cancelled: %v", err)
	}
	if _, err := confirmRuntimeMutation(t.Context(), r, operation); err != nil {
		t.Fatal(err)
	}
	if err := cancelRuntimePreparation(t.Context(), r, operation); !errors.Is(err, ErrRuntimeConflict) {
		t.Fatalf("applied mutation cancelled: %v", err)
	}
}

func TestRuntimeChangedConfigRequiresNewNativeProcess(t *testing.T) {
	pending := runtimePending{FromConfigSHA256: strings.Repeat("a", 64), ConfigSHA256: strings.Repeat("b", 64), BeforeProcess: strings.Repeat("a", 32)}
	for _, observation := range []TunnelObservation{{Ready: true, Process: strings.Repeat("a", 32)}, {Ready: true}, {Ready: false, Process: strings.Repeat("b", 32)}} {
		if runtimeMutationReady(pending, observation) {
			t.Fatalf("unproven loaded config accepted: %v", observation)
		}
	}
	if !runtimeMutationReady(pending, TunnelObservation{Ready: true, Process: strings.Repeat("b", 32)}) {
		t.Fatal("new native invocation refused")
	}
	pending.ConfigSHA256 = pending.FromConfigSHA256
	if !runtimeMutationReady(pending, TunnelObservation{Ready: true, Process: pending.BeforeProcess}) {
		t.Fatal("unchanged configuration required pointless restart")
	}
}

func TestPinnedRuntimeCombinedConfigValidationUsesAnonymousDescriptor(t *testing.T) {
	command := os.Getenv("PANEL_TEST_CLOUDFLARED")
	if command == "" {
		t.Skip("PANEL_TEST_CLOUDFLARED must name pinned cloudflared2026.10.3")
	}
	operation := runtimeOperationFixture(t, environmentA, "app.example.com")
	r := runtimeRootsFixture(t, operation)
	prepared, err := prepareRuntime(t.Context(), r, operation)
	if err != nil {
		t.Fatal(err)
	}
	_, configuration, err := planRuntimeApply(r.ledger, operation, prepared.Revision, prepared.ConfigSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateTunnelConfiguration(t.Context(), r.connector, configuration, command); err != nil {
		t.Fatalf("pinned native ingress rejected combined config: %v", err)
	}
}
