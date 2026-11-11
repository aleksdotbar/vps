package deployment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/aleksdotbar/vps/panel/internal/compose"
	"github.com/aleksdotbar/vps/panel/internal/releases"
	"github.com/aleksdotbar/vps/panel/internal/routes"
)

const fixtureFinal = "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-nixos-system-fixture"

func stageFixture(t *testing.T, repeated ...int) (StageRequest, validationTool) {
	t.Helper()
	command := os.Getenv("PANEL_TEST_COMPOSE")
	if command == "" {
		command = "docker-compose"
	}
	resolver, err := compose.New(command)
	if err != nil {
		t.Fatal(err)
	}
	artifactData, err := os.ReadFile("../../../examples/stateless/release.json")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := releases.ParseArtifact(artifactData, "0123456789abcdef0123456789abcdef01234567")
	if err != nil {
		t.Fatal(err)
	}
	tree := t.TempDir()
	sourceRoot, err := os.OpenRoot("../../../examples/stateless")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sourceRoot.Close() }()
	targetRoot, err := os.OpenRoot(tree)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = targetRoot.Close() }()
	for source, target := range map[string]string{"compose.yaml": "compose.yaml", "compose.staging.yaml": "compose.staging.yaml"} {
		data, err := sourceRoot.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if source == "compose.yaml" && len(repeated) != 0 {
			var references strings.Builder
			for i := 0; i < repeated[0]; i++ {
				fmt.Fprintf(&references, "      REPEATED_%d: ${API_TOKEN}\n", i)
			}
			data = bytes.Replace(data, []byte("    environment:\n"), []byte("    environment:\n"+references.String()), 1)
		}
		data = bytes.ReplaceAll(data, []byte("${WORKER_TOKEN:?set in the panel}"), []byte("${WORKER_TOKEN}"))
		data = bytes.ReplaceAll(data, []byte("    logging:\n      driver: json-file\n      options:\n        max-size: 10m\n        max-file: \"3\"\n"), nil)
		if err := targetRoot.WriteFile(target, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	identity := Identity{InstallationID: "11111111-1111-4111-8111-111111111111", ServerID: "22222222-2222-4222-8222-222222222222", EnvironmentID: "33333333-3333-4333-8333-333333333333", OperationID: "44444444-4444-4444-8444-444444444444", ReleaseID: "55555555-5555-4555-8555-555555555555", BindingVersion: 1, ExecutionDeadline: time.Now().Add(30 * time.Minute).UnixMilli()}
	prepared, err := resolver.Resolve(t.Context(), tree, "compose.staging.yaml", artifact, map[string]bool{"API_TOKEN": true, "WORKER_TOKEN": false}, compose.Network{ProjectName: "vps-33333333333343338333333333333333", BridgeName: "vps-1234567890"})
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Identity: identity, EnvironmentVersion: 1, SecretVersion: 1, RouteSnapshotSHA256: strings.Repeat("a", 64), BridgeName: "vps-1234567890", Artifact: artifact, Config: prepared.Config, Domains: nil, Bindings: prepared.Bindings}
	manifest.RouteSnapshot = routes.Snapshot{SchemaVersion: 1, OperationID: identity.OperationID, ReleaseID: identity.ReleaseID, EnvironmentID: identity.EnvironmentID, ServerID: identity.ServerID, Services: prepared.Services, Bindings: []routes.Binding{}}
	manifest.RouteSnapshotSHA256, err = routes.SnapshotSHA256(manifest.RouteSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ReleaseSHA256, err = releases.SnapshotSHA256(manifest.Config, []byte("null"), identity.EnvironmentID, 1)
	if err != nil {
		t.Fatal(err)
	}
	request := StageRequest{SchemaVersion: 1, Action: "deploy_stage", Challenge: strings.Repeat("b", 64), Manifest: manifest, SecretValues: map[string][]byte{"API_TOKEN": []byte("private$token\n"), "WORKER_TOKEN": {}}}
	t.Cleanup(request.Clear)
	resolved, err := filepath.Abs(command)
	if !strings.Contains(command, "/") {
		resolved, err = exec.LookPath(command)
	}
	if err != nil {
		t.Fatal(err)
	}
	tool := validationTool{resolved, nil}
	if docker := os.Getenv("PANEL_TEST_DOCKER"); docker != "" {
		tool = validationTool{docker, []string{"compose"}}
	}
	return request, tool
}

func stateRoot(t *testing.T) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if err := root.Chmod(".", 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestMaterializationKeepsDeclaredServiceIsolationAndDollarSemantics(t *testing.T) {
	request, _ := stageFixture(t)
	data, err := Materialize(request)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(data)
	var model struct {
		Services map[string]struct {
			Environment map[string]string `json:"environment"`
		} `json:"services"`
	}
	if json.Unmarshal(data, &model) != nil {
		t.Fatal("invalid materialized config")
	}
	if model.Services["app"].Environment["API_TOKEN"] != "private$$token\n" || model.Services["worker"].Environment["WORKER_TOKEN"] != "" {
		t.Fatal("inserted dollar or explicit empty value changed")
	}
	if _, leaked := model.Services["worker"].Environment["API_TOKEN"]; leaked {
		t.Fatal("secret crossed service boundary")
	}
	request.SecretValues["UNDECLARED"] = []byte("never supplied")
	if _, err := Materialize(request); !errors.Is(err, ErrInvalid) {
		t.Fatal("unreferenced secret accepted")
	}
	delete(request.SecretValues, "UNDECLARED")
	request.Manifest.Bindings[0].Variable = "LOG_LEVEL"
	if _, err := Materialize(request); !errors.Is(err, ErrInvalid) {
		t.Fatal("binding reassignment accepted")
	}
}

func TestStageReplayExclusionAndReadOnlyStatus(t *testing.T) {
	request, tool := stageFixture(t)
	root := stateRoot(t)
	first, err := stage(t.Context(), root, request, fixtureFinal, tool, time.Now, nil, nil)
	if err != nil || first.Ready || first.Phase != "staged" {
		t.Fatalf("stage: %v", err)
	}
	request.Challenge = strings.Repeat("c", 64)
	second, err := stage(t.Context(), root, request, fixtureFinal, validationTool{}, time.Now, nil, nil)
	if err != nil || second.ManifestSHA256 != first.ManifestSHA256 || second.Challenge != request.Challenge {
		t.Fatalf("complete replay: %v", err)
	}
	request.SecretValues["API_TOKEN"] = []byte("changed")
	if _, err := stage(t.Context(), root, request, fixtureFinal, tool, time.Now, nil, nil); !errors.Is(err, ErrState) {
		t.Fatal("same intent changed secret material")
	}
	request.SecretValues["API_TOKEN"] = []byte("private$token\n")
	for _, change := range []func(*Identity){func(value *Identity) { value.OperationID = "66666666-6666-4666-8666-666666666666" }, func(value *Identity) { value.ExecutionDeadline-- }, func(value *Identity) { value.BindingVersion++ }, func(value *Identity) { value.ExpectedCurrentReleaseID = value.ReleaseID }} {
		request.Manifest.Identity = first.Identity
		change(&request.Manifest.Identity)
		request.Manifest.RouteSnapshot.OperationID = request.Manifest.Identity.OperationID
		request.Manifest.RouteSnapshotSHA256, _ = routes.SnapshotSHA256(request.Manifest.RouteSnapshot)
		if _, err := stage(t.Context(), root, request, fixtureFinal, tool, time.Now, nil, nil); !errors.Is(err, ErrBusy) {
			t.Fatal("conflicting operation or altered deadline entered environment")
		}
	}
	request.Manifest.Identity.ExecutionDeadline = time.Now().Add(-time.Second).UnixMilli()
	if _, err := stage(t.Context(), root, request, fixtureFinal, tool, time.Now, nil, nil); !errors.Is(err, ErrDeadline) {
		t.Fatal("expired operation admitted staging")
	}
	request.Manifest.Identity = first.Identity
	request.Manifest.RouteSnapshot.OperationID = first.Identity.OperationID
	request.Manifest.RouteSnapshotSHA256, _ = routes.SnapshotSHA256(request.Manifest.RouteSnapshot)
	environment, err := root.OpenRoot(first.Identity.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = environment.Close() }()
	lock, err := environment.OpenFile("effect.lock", os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		t.Fatal("fixture exclusion unavailable")
	}
	if _, err := stage(t.Context(), root, request, fixtureFinal, tool, time.Now, nil, nil); !errors.Is(err, ErrBusy) {
		t.Fatal("concurrent mutation bypassed exclusion")
	}
	statusRequest := StatusRequest{1, "deploy_status", request.Challenge, first.Identity, first.ManifestSHA256}
	status, err := Status(root, statusRequest, fixtureFinal)
	if err != nil || status.Phase != "staged" || status.Ready {
		t.Fatalf("status blocked on effect lock or claimed readiness: %v", err)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, request.SecretValues["API_TOKEN"]) {
		t.Fatal("status exposed application secret")
	}
	if _, err := ParseResponse(encoded, statusRequest.Action, statusRequest.Challenge, statusRequest.Identity, statusRequest.ManifestSHA256); err != nil {
		t.Fatal(err)
	}
}

func TestPartialAndUnprotectedStagingRefuseReplay(t *testing.T) {
	for _, failure := range []string{"missing stage", "public stage", "symlink stage"} {
		t.Run(failure, func(t *testing.T) {
			request, tool := stageFixture(t)
			root := stateRoot(t)
			staged, err := stage(t.Context(), root, request, fixtureFinal, tool, time.Now, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			environment, err := root.OpenRoot(request.Manifest.Identity.EnvironmentID)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = environment.Close() }()
			operation, err := operationRoot(environment, request.Manifest.Identity.OperationID, false)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = operation.Close() }()
			if failure == "public stage" {
				err = operation.Chmod("stage.json", 0644)
			} else {
				err = operation.Remove("stage.json")
				if err == nil && failure == "symlink stage" {
					err = operation.Symlink("active.json", "stage.json")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stage(t.Context(), root, request, fixtureFinal, tool, time.Now, nil, nil); !errors.Is(err, ErrState) {
				t.Fatalf("%s accepted retry: %v", failure, err)
			}
			status, err := Status(root, StatusRequest{1, "deploy_status", request.Challenge, staged.Identity, staged.ManifestSHA256}, fixtureFinal)
			if err != nil || status.Phase != "needs_attention" || status.Reason != "staging_incomplete" || status.Ready {
				t.Fatalf("%s lost partial observation: %v", failure, err)
			}
		})
	}
}

func TestNativeValidationRefusalLeavesNoActiveClaim(t *testing.T) {
	request, tool := stageFixture(t)
	root := stateRoot(t)
	var model map[string]json.RawMessage
	if json.Unmarshal(request.Manifest.Config, &model) != nil || model == nil {
		t.Fatal("fixture config")
	}
	var services map[string]map[string]json.RawMessage
	if json.Unmarshal(model["services"], &services) != nil || services["app"] == nil {
		t.Fatal("fixture services")
	}
	services["app"]["depends_on"] = json.RawMessage(`{"missing":{"condition":"service_started"}}`)
	model["services"], _ = json.Marshal(services)
	request.Manifest.Config, _ = json.Marshal(model)
	request.Manifest.ReleaseSHA256, _ = releases.SnapshotSHA256(request.Manifest.Config, []byte("null"), request.Manifest.Identity.EnvironmentID, 1)
	if _, err := stage(t.Context(), root, request, fixtureFinal, tool, time.Now, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid native service dependency admitted: %v", err)
	}
	environment, err := root.OpenRoot(request.Manifest.Identity.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = environment.Close() }()
	if directoryEntries(environment, "effect.lock") != nil {
		t.Fatal("failed native validation published active authority")
	}
}

func TestMaterializationBoundsRepeatedEscapedSecretsBeforeExpansion(t *testing.T) {
	request, _ := stageFixture(t, 80)
	request.SecretValues["API_TOKEN"] = bytes.Repeat([]byte("$"), 16<<10)
	if _, err := Materialize(request); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized repeated secret expansion accepted: %v", err)
	}
	clear(request.SecretValues["API_TOKEN"])
	request.SecretValues["API_TOKEN"] = bytes.Repeat([]byte("a"), 16<<10)
	data, err := Materialize(request)
	defer clear(data)
	if err != nil || len(data) > MaxApplicationBytes {
		t.Fatalf("bounded ASCII secret rejected: %v", err)
	}
}

func TestStageDeadlineBoundsValidationPublicationAndReplayObservation(t *testing.T) {
	request, tool := stageFixture(t)
	deadline := time.UnixMilli(request.Manifest.Identity.ExecutionDeadline)
	root := stateRoot(t)
	calls := 0
	advancingClock := func() time.Time {
		calls++
		if calls == 1 {
			return deadline.Add(-time.Minute)
		}
		return deadline
	}
	if _, err := stage(t.Context(), root, request, fixtureFinal, tool, advancingClock, nil, nil); !errors.Is(err, ErrDeadline) {
		t.Fatalf("validation crossing deadline admitted publication: %v", err)
	}
	environment, err := root.OpenRoot(request.Manifest.Identity.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = environment.Close() }()
	if directoryEntries(environment, "effect.lock") != nil {
		t.Fatal("expired validation published active claim")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := stage(canceled, root, request, fixtureFinal, tool, time.Now, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled caller entered staging: %v", err)
	}
	if _, err := stage(t.Context(), root, request, fixtureFinal, tool, time.Now, nil, nil); err != nil {
		t.Fatal(err)
	}
	calls = 0
	observed, err := stage(t.Context(), root, request, fixtureFinal, validationTool{}, advancingClock, nil, nil)
	if err != nil || observed.Phase != "needs_attention" || observed.Reason != "execution_deadline_exceeded" || observed.Ready {
		t.Fatalf("replay returned stale staged state after deadline: %v", err)
	}
}
