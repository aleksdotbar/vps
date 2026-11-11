package deployment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/routes"
	"github.com/google/uuid"
)

func materialFixture(t *testing.T) (StageRequest, releaseMaterial) {
	t.Helper()
	request, _ := stageFixture(t)
	hash, err := ManifestSHA256(request.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	application, err := Materialize(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clear(application) })
	stage := staged{Claim: claim{request.Manifest.Identity, hash, fixtureFinal}, Manifest: request.Manifest, ApplicationConfig: application}
	origins := []routes.Origin{{EnvironmentID: request.Manifest.Identity.EnvironmentID, Service: "app", Port: 3000, LoopbackPort: 31000}}
	config, err := runtimeCompose(stage, origins)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clear(config) })
	images := map[string]string{}
	for service := range stage.Manifest.Artifact.Services {
		images[service] = "sha256:" + strings.Repeat("e", 64)
	}
	return request, releaseMaterial{stage, config, origins, images}
}

func TestContainerProofRejectsForeignIdentityVolumesAndPublicBindings(t *testing.T) {
	_, material := materialFixture(t)
	identity := material.Stage.Claim.Identity
	policies, err := containerPolicies(material.Stage.Manifest.Config)
	if err != nil {
		t.Fatal(err)
	}
	observed := containerObservation{ID: strings.Repeat("d", 64), Image: material.ImageIDs["app"], Reference: material.Stage.Manifest.Artifact.Services["app"].Image, Labels: map[string]string{"com.docker.compose.service": "app", "com.docker.compose.project": project(identity), "com.docker.compose.oneoff": "False", "vps.installation": identity.InstallationID, "vps.environment": identity.EnvironmentID, "vps.release": identity.ReleaseID, "vps.operation": identity.OperationID, "vps.manifest": material.Stage.Claim.ManifestSHA256}, State: "running", Health: "healthy", ReadOnly: true, User: policies["app"].User, Networks: map[string]json.RawMessage{project(identity) + "_default": json.RawMessage(`{}`)}}
	observed.Tmpfs = map[string]string{"/tmp": "size=16777216,noexec,nosuid"}
	observed.ConfiguredPorts = map[string][]portBinding{"3000/tcp": {{HostIP: "127.0.0.1", HostPort: "31000"}}}
	if json.Unmarshal([]byte(`{"3000/tcp":[{"HostIP":"127.0.0.1","HostPort":"31000"}]}`), &observed.Ports) != nil {
		t.Fatal("fixture metadata")
	}
	if _, err := validateContainer(observed, material, policies["app"]); err != nil {
		t.Fatal(err)
	}
	stopped := observed
	stopped.State, stopped.Health, stopped.Ports = "exited", "", nil
	if _, err := validateContainer(stopped, material, policies["app"]); err != nil {
		t.Fatal("stopped owned candidate refused recovery")
	}
	if containerPortsMatch(stopped.Ports, material.Origins, "app") {
		t.Fatal("stopped publication claimed readiness")
	}
	encoded, _ := json.Marshal(observed)
	for _, change := range []struct {
		name  string
		apply func(*containerObservation)
	}{
		{"foreign operation", func(value *containerObservation) { value.Labels["vps.operation"] = uuid.NewString() }},
		{"wrong image", func(value *containerObservation) { value.Image = "sha256:" + strings.Repeat("f", 64) }},
		{"writable root", func(value *containerObservation) { value.ReadOnly = false }},
		{"privileged", func(value *containerObservation) { value.Privileged = true }},
		{"image volume", func(value *containerObservation) {
			value.Mounts = append(value.Mounts, struct {
				Type        string
				Destination string
			}{"volume", "/data"})
		}},
		{"tmpfs options", func(value *containerObservation) { value.Tmpfs["/tmp"] = "size=16777216" }},
		{"public binding", func(value *containerObservation) {
			ports := value.ConfiguredPorts["3000/tcp"]
			ports[0].HostIP = "0.0.0.0"
			value.ConfiguredPorts["3000/tcp"] = ports
		}},
		{"foreign network", func(value *containerObservation) { value.Networks["bridge"] = json.RawMessage(`{}`) }},
	} {
		t.Run(change.name, func(t *testing.T) {
			var changed containerObservation
			if json.Unmarshal(encoded, &changed) != nil {
				t.Fatal("fixture")
			}
			change.apply(&changed)
			if _, err := validateContainer(changed, material, policies["app"]); !errors.Is(err, ErrState) {
				t.Fatalf("foreign runtime admitted: %v", err)
			}
		})
	}
}

func TestNextOperationRetainsExactRollbackMaterialWithoutRegistryToken(t *testing.T) {
	request, tool := stageFixture(t)
	request.Registry = &RegistryCredential{Username: "fixture", Token: []byte("synthetic-registry-token")}
	root := stateRoot(t)
	registryRoot := stateRoot(t)
	stagedResponse, err := stage(t.Context(), root, request, fixtureFinal, tool, time.Now, registryRoot, func(context.Context, registryExpiry) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	environment, err := root.OpenRoot(request.Manifest.Identity.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = environment.Close() }()
	retained, err := readStage(environment, claim{request.Manifest.Identity, stagedResponse.ManifestSHA256, fixtureFinal})
	defer clear(retained.ApplicationConfig)
	if err != nil {
		t.Fatal(err)
	}
	original := releaseReference{request.Manifest.Identity, stagedResponse.ManifestSHA256}
	config, err := runtimeCompose(retained, nil)
	defer clear(config)
	if err != nil {
		t.Fatal(err)
	}
	images := map[string]string{}
	for service := range retained.Manifest.Artifact.Services {
		images[service] = "sha256:" + strings.Repeat("e", 64)
	}
	if saveMaterial(t.Context(), environment, releaseMaterial{retained, config, nil, images}) != nil {
		t.Fatal("retain release")
	}
	previous := execution{Claim: retained.Claim, LaunchID: uuid.NewString(), BootID: uuid.NewString(), InvocationID: strings.Repeat("a", 32), Phase: "succeeded"}
	if putRecord(t.Context(), environment, "current.json", original) != nil || putExecution(t.Context(), environment, previous) != nil {
		t.Fatal("terminal fixture")
	}
	if cleanupRegistry(registryRoot, request.Manifest.Identity.OperationID) != nil {
		t.Fatal("registry cleanup")
	}
	clear(request.Registry.Token)
	request.Registry = nil
	request.Manifest.Identity.OperationID = uuid.NewString()
	request.Manifest.Identity.ExpectedCurrentReleaseID = original.Identity.ReleaseID
	request.Manifest.RouteSnapshot.OperationID = request.Manifest.Identity.OperationID
	request.Manifest.RouteSnapshotSHA256, _ = routes.SnapshotSHA256(request.Manifest.RouteSnapshot)
	// Same release under a different operation remains legal rollback/redeploy.
	if _, err := stage(t.Context(), root, request, fixtureFinal, tool, time.Now, registryRoot, func(context.Context, registryExpiry) error { return nil }); err != nil {
		t.Fatalf("new operation could not retain existing release: %v", err)
	}
	if saveConfirmation(t.Context(), environment, retained.Claim) != nil {
		t.Fatal("late previous confirmation")
	}
	newHash, _ := ManifestSHA256(request.Manifest)
	confirmed, err := readConfirmation(environment, claim{request.Manifest.Identity, newHash, fixtureFinal})
	if err != nil || confirmed {
		t.Fatal("late previous confirmation authorized new operation")
	}
	current, err := readCurrent(environment)
	if err != nil || current == nil || *current != original {
		t.Fatal("staging promoted current release")
	}
	old, err := material(environment, original)
	defer clear(old.RuntimeConfig)
	defer clear(old.Stage.ApplicationConfig)
	if err != nil || !bytes.Equal(old.RuntimeConfig, config) {
		t.Fatal("previous material changed")
	}
	retainedOperation, err := operationRoot(environment, original.Identity.OperationID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = retainedOperation.Close() }()
	retainedData, err := retainedOperation.ReadFile("stage.json")
	defer clear(retainedData)
	if err != nil || bytes.Contains(retainedData, []byte("synthetic-registry-token")) {
		t.Fatal("registry token retained in immutable stage")
	}
	if _, err := registryRoot.Lstat(original.Identity.OperationID); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("registry token retained after pull")
	}
}

func TestExecutionProtocolRejectsForgedHealthyServiceAndChangedChallenge(t *testing.T) {
	request, material := materialFixture(t)
	control := ExecutionRequest{1, "deploy_observe", request.Challenge, request.Manifest.Identity, material.Stage.Claim.ManifestSHA256}
	value := execution{Claim: material.Stage.Claim, Phase: "succeeded"}
	response := executionResponse(control, value)
	response.Services = []ServiceObservation{{Service: "app", Image: material.Stage.Manifest.Artifact.Services["app"].Image, ContainerID: strings.Repeat("d", 64), State: "running", Health: "healthy"}}
	encoded, _ := json.Marshal(response)
	if _, err := ParseExecutionResponse(encoded, control); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*ExecutionResponse){func(value *ExecutionResponse) { value.Services[0].Health = "starting" }, func(value *ExecutionResponse) { value.Challenge = strings.Repeat("c", 64) }, func(value *ExecutionResponse) { value.ActiveReleaseID = uuid.NewString() }, func(value *ExecutionResponse) { value.Reason = "raw host error" }} {
		var changed ExecutionResponse
		if json.Unmarshal(encoded, &changed) != nil {
			t.Fatal("fixture response")
		}
		change(&changed)
		data, _ := json.Marshal(changed)
		if _, err := ParseExecutionResponse(data, control); err == nil {
			t.Fatal("forged terminal proof accepted")
		}
	}
}

func TestNeutralRoutesRequireBothCandidateAndRetainedPreviousDomainless(t *testing.T) {
	request, _ := stageFixture(t)
	candidate := staged{Manifest: request.Manifest}
	neutral := routes.RuntimeReceipt{OperationID: request.Manifest.Identity.OperationID, ReleaseID: request.Manifest.Identity.ReleaseID, EnvironmentID: request.Manifest.Identity.EnvironmentID, RouteSnapshotSHA256: request.Manifest.RouteSnapshotSHA256, Phase: routes.RuntimeNeutral}
	if !routeReceiptMatches(neutral, candidate, nil, false) || !routeReceiptMatches(neutral, candidate, nil, true) {
		t.Fatal("domainless first release required nonexistent route authority")
	}
	previous := releaseMaterial{Stage: candidate}
	if !routeReceiptMatches(neutral, candidate, &previous, true) {
		t.Fatal("domainless previous recovery refused neutral route result")
	}
	previous.Stage.Manifest.RouteSnapshot.Bindings = []routes.Binding{{Service: "app"}}
	if routeReceiptMatches(neutral, candidate, &previous, true) || routeReceiptMatches(neutral, candidate, &previous, false) {
		t.Fatal("neutral route proof discarded retained public contribution")
	}
	candidate.Manifest.RouteSnapshot.Bindings = []routes.Binding{{Service: "app"}}
	if routeReceiptMatches(neutral, candidate, nil, false) {
		t.Fatal("public candidate accepted neutral route proof")
	}
	candidate.Manifest.RouteSnapshot.Bindings = nil
	neutral.ActiveReleaseID = request.Manifest.Identity.ReleaseID
	if routeReceiptMatches(neutral, candidate, nil, false) {
		t.Fatal("neutral receipt claimed active release authority")
	}
}

func TestInterruptedStagePublicationResumesThroughSingleActivePointer(t *testing.T) {
	request, tool := stageFixture(t)
	registryRoot := stateRoot(t)
	request.Registry = &RegistryCredential{Username: "fixture", Token: []byte("synthetic-private-pull-token")}
	defer request.Clear()
	arm := func(context.Context, registryExpiry) error { return nil }
	root := stateRoot(t)
	first, err := stage(t.Context(), root, request, fixtureFinal, tool, time.Now, registryRoot, arm)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := root.OpenRoot(request.Manifest.Identity.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = environment.Close() }()
	original := claim{request.Manifest.Identity, first.ManifestSHA256, fixtureFinal}
	directory, err := child(registryRoot, request.Manifest.Identity.OperationID, false)
	if err != nil {
		t.Fatal(err)
	}
	before, err := readRegistryExpiry(directory, request.Manifest.Identity.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	// Simulate interruption after immutable stage publication, before first pointer.
	if err := environment.Remove("active.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := stage(t.Context(), root, request, fixtureFinal, tool, time.Now, registryRoot, arm); err != nil {
		t.Fatalf("initial publication could not resume: %v", err)
	}
	after, err := readRegistryExpiry(directory, request.Manifest.Identity.OperationID)
	if err != nil || after != before {
		t.Fatalf("registry retry changed original expiry: %v", err)
	}
	previous := execution{Claim: original, LaunchID: uuid.NewString(), BootID: uuid.NewString(), InvocationID: strings.Repeat("a", 32), Phase: "failed"}
	if err := putExecution(t.Context(), environment, previous); err != nil {
		t.Fatal(err)
	}
	request.Manifest.Identity.OperationID = uuid.NewString()
	request.Manifest.RouteSnapshot.OperationID = request.Manifest.Identity.OperationID
	request.Manifest.RouteSnapshotSHA256, _ = routes.SnapshotSHA256(request.Manifest.RouteSnapshot)
	hash, err := ManifestSHA256(request.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	application, err := Materialize(request)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(application)
	proof, data, err := registryProof(request.Registry)
	clear(data)
	if err != nil {
		t.Fatal(err)
	}
	next := staged{RegistrySHA256: proof, Claim: claim{request.Manifest.Identity, hash, fixtureFinal}, Manifest: request.Manifest, ApplicationConfig: application}
	if err := retainStage(t.Context(), environment, next); err != nil {
		t.Fatal(err)
	}
	if err := putRecord(t.Context(), environment, ".next", next.Claim); err != nil {
		t.Fatal(err)
	}
	// Interrupted replacement must leave the old pointer authoritative until replay.
	var observed claim
	if readRecord(environment, "active.json", &observed) != nil || observed != original {
		t.Fatal("uncommitted candidate changed active operation")
	}
	if _, err := stage(t.Context(), root, request, fixtureFinal, tool, time.Now, registryRoot, arm); err != nil {
		t.Fatalf("replacement publication could not resume: %v", err)
	}
	if readRecord(environment, "active.json", &observed) != nil || observed != next.Claim {
		t.Fatal("resumed operation did not become active")
	}
	replacementRegistry, err := child(registryRoot, next.Claim.Identity.OperationID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replacementRegistry.Close() }()
	replacementExpiry, err := readRegistryExpiry(replacementRegistry, next.Claim.Identity.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacementRegistry.Remove("credential.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := stage(t.Context(), root, request, fixtureFinal, tool, time.Now, registryRoot, arm); err != nil {
		t.Fatalf("private active stage could not resume credential publication: %v", err)
	}
	resumedExpiry, err := readRegistryExpiry(replacementRegistry, next.Claim.Identity.OperationID)
	if err != nil || replacementExpiry != resumedExpiry {
		t.Fatal("private replacement renewed credential expiry")
	}
	if _, err := readExecution(environment, next.Claim); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("previous execution reused for new operation: %v", err)
	}
}

func TestRetirementPreservesReferencedAndUnresolvedOperationMaterial(t *testing.T) {
	request, tool := stageFixture(t)
	root := stateRoot(t)
	first, err := stage(t.Context(), root, request, fixtureFinal, tool, time.Now, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := root.OpenRoot(request.Manifest.Identity.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = environment.Close() }()
	keep := map[string]bool{request.Manifest.Identity.OperationID: true}
	retired, unresolved, retained := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, operationID := range []string{retired, unresolved, retained} {
		operation, err := operationRoot(environment, operationID, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := operation.Close(); err != nil {
			t.Fatal(err)
		}
		identity := request.Manifest.Identity
		identity.OperationID = operationID
		phase := "succeeded"
		if operationID == unresolved {
			phase = "needs_attention"
		}
		value := execution{Claim: claim{identity, first.ManifestSHA256, fixtureFinal}, LaunchID: uuid.NewString(), BootID: uuid.NewString(), InvocationID: strings.Repeat("b", 32), Phase: phase}
		if err := putExecution(t.Context(), environment, value); err != nil {
			t.Fatal(err)
		}
	}
	keep[retained] = true
	if err := pruneOperations(t.Context(), environment, request.Manifest.Identity, keep); err != nil {
		t.Fatal(err)
	}
	operations, err := child(environment, "operations", false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = operations.Close() }()
	for _, operationID := range []string{request.Manifest.Identity.OperationID, unresolved, retained} {
		if _, err := operations.Stat(operationID); err != nil {
			t.Fatalf("required operation %s retired: %v", operationID, err)
		}
	}
	if _, err := operations.Stat(retired); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced resolved material remained: %v", err)
	}
}
