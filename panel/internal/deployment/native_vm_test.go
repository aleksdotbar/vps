package deployment_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aleksdotbar/vps/panel/internal/compose"
	"github.com/aleksdotbar/vps/panel/internal/deployment"
	"github.com/aleksdotbar/vps/panel/internal/hosthelper"
	"github.com/aleksdotbar/vps/panel/internal/releases"
	"github.com/aleksdotbar/vps/panel/internal/routes"
)

// This destructive fixture runs only inside the disposable NixOS test VM.
func TestNativeDeploymentAndRecovery(t *testing.T) {
	if os.Getenv("PANEL_NATIVE_VM") != "1" {
		t.Skip("requires disposable native deployment VM")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	system, err := filepath.EvalSymlinks("/run/current-system")
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile("/root/fixture-image")
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot("/var/lib/panel-deploy")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	acquire := func(ctx context.Context) (func() error, error) {
		lease, err := hosthelper.AcquireHost(ctx)
		if err != nil {
			return nil, err
		}
		return lease.Release, nil
	}
	resolver, err := compose.New(system + "/sw/bin/docker-compose")
	if err != nil {
		t.Fatal(err)
	}
	environment := uuid.NewString()
	makeRequest := func(previous, command, startPeriod string) deployment.ExecutionRequest {
		t.Helper()
		identity := deployment.Identity{InstallationID: "11111111-1111-4111-8111-111111111111", ServerID: "22222222-2222-4222-8222-222222222222", EnvironmentID: environment, OperationID: uuid.NewString(), ReleaseID: uuid.NewString(), BindingVersion: 1, ExpectedCurrentReleaseID: previous, ExecutionDeadline: time.Now().Add(6*time.Minute + 40*time.Second).UnixMilli()}
		artifact := releases.Artifact{SchemaVersion: 1, SourceSHA: strings.Repeat("a", 40), Services: map[string]releases.ArtifactService{"app": {ImageVariable: "APP_IMAGE", Image: strings.TrimSpace(string(image))}}}
		artifactData, err := json.Marshal(artifact)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := releases.ParseArtifact(artifactData, artifact.SourceSHA); err != nil {
			t.Fatal("fixture registry reference:", string(image), err)
		}
		tree := t.TempDir()
		definition := fmt.Sprintf(`services:
  app:
    image: ${APP_IMAGE}
    command: %s
    user: "1000:1000"
    init: true
    read_only: true
    cap_drop: [ALL]
    security_opt: [no-new-privileges:true]
    cpus: 0.5
    mem_limit: 64m
    pids_limit: 32
    stop_grace_period: 1s
    environment:
      API_TOKEN: ${API_TOKEN}
    healthcheck:
      test: [CMD, /bin/wget, -q, -O, /dev/null, http://127.0.0.1:8080/]
      interval: 1s
      timeout: 1s
      retries: 1
      start_period: %s
`, command, startPeriod)
		for name, data := range map[string]string{"compose.yaml": definition, "compose.staging.yaml": "services: {}\n"} {
			if err := os.WriteFile(filepath.Join(tree, name), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
		}
		prepared, err := resolver.Resolve(ctx, tree, "compose.staging.yaml", artifact, map[string]bool{"API_TOKEN": true}, compose.Network{ProjectName: "vps-" + strings.ReplaceAll(environment, "-", ""), BridgeName: "vps-1234567890"})
		if err != nil {
			t.Fatal("resolve:", err)
		}
		snapshot := routes.Snapshot{SchemaVersion: 1, OperationID: identity.OperationID, ReleaseID: identity.ReleaseID, EnvironmentID: environment, ServerID: identity.ServerID, Services: prepared.Services, Bindings: []routes.Binding{}}
		routeHash, err := routes.SnapshotSHA256(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		releaseHash, err := releases.SnapshotSHA256(prepared.Config, []byte("null"), environment, 1)
		if err != nil {
			t.Fatal(err)
		}
		manifest := deployment.Manifest{Identity: identity, EnvironmentVersion: 1, SecretVersion: 1, ReleaseSHA256: releaseHash, RouteSnapshotSHA256: routeHash, RouteSnapshot: snapshot, BridgeName: "vps-1234567890", Artifact: artifact, Config: prepared.Config, Bindings: prepared.Bindings}
		stage := deployment.StageRequest{SchemaVersion: 1, Action: "deploy_stage", Challenge: strings.Repeat("b", 64), Manifest: manifest, SecretValues: map[string][]byte{"API_TOKEN": []byte("synthetic$token")}}
		defer stage.Clear()
		staged, err := deployment.Stage(ctx, root, stage, system)
		if err != nil {
			t.Fatal("stage:", err)
		}
		return deployment.ExecutionRequest{SchemaVersion: 1, Action: "deploy_start", Challenge: stage.Challenge, Identity: identity, ManifestSHA256: staged.ManifestSHA256}
	}
	wait := func(request deployment.ExecutionRequest, phase string) deployment.ExecutionResponse {
		t.Helper()
		request.Action = "deploy_observe"
		last := ""
		for ctx.Err() == nil {
			observed, err := deployment.Observe(ctx, root, request, system)
			if err == nil {
				if observed.Phase != last {
					t.Log("phase:", observed.Phase, observed.Reason)
					last = observed.Phase
				}
				if observed.Phase == phase {
					return observed
				}
				if observed.Phase == "needs_attention" || observed.Phase == "failed" {
					t.Fatalf("unexpected outcome: %s %s", observed.Phase, observed.Reason)
				}
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("waiting for %s: last phase %s", phase, last)
		return deployment.ExecutionResponse{}
	}
	start := func(request deployment.ExecutionRequest) {
		t.Helper()
		if _, err := deployment.Start(ctx, root, request, system, acquire); err != nil {
			t.Fatal("start:", err)
		}
	}
	first := makeRequest("", `["/bin/httpd", "-f", "-p", "8080", "-h", "/www"]`, "1s")
	start(first)
	wait(first, "awaiting_dns")
	first.Action = "deploy_confirm"
	if _, err := deployment.Confirm(ctx, root, first, system, acquire); err != nil {
		t.Fatal("confirm:", err)
	}
	wait(first, "succeeded")
	// Repeating a request after losing its response must observe, not replace.
	first.Action = "deploy_start"
	start(first)
	project := "vps-" + strings.ReplaceAll(environment, "-", "")
	before := nativeVMCommand(t, ctx, "docker", "ps", "--quiet", "--filter", "label=com.docker.compose.project="+project)
	if actual := nativeVMCommand(t, ctx, "docker", "exec", before, "/bin/printenv", "API_TOKEN"); actual != "synthetic$token" {
		t.Fatal("runtime secret changed during Compose interpolation")
	}
	start(first)
	if after := nativeVMCommand(t, ctx, "docker", "ps", "--quiet", "--filter", "label=com.docker.compose.project="+project); before != after {
		t.Fatal("duplicate start replaced a confirmed container")
	}
	bad := makeRequest(first.Identity.ReleaseID, `["/bin/sleep", "600"]`, "1s")
	start(bad)
	recovered := wait(bad, "recovered")
	if recovered.ActiveReleaseID != first.Identity.ReleaseID {
		t.Fatal("failed replacement did not restore previous release")
	}
	interrupted := makeRequest(first.Identity.ReleaseID, `["/bin/sleep", "600"]`, "60s")
	start(interrupted)
	wait(interrupted, "checking")
	nativeVMCommand(t, ctx, "systemctl", "kill", "--signal=SIGKILL", "--kill-whom=main", "panel-deploy-"+interrupted.Identity.OperationID+".service")
	for {
		state := nativeVMCommand(t, ctx, "systemctl", "show", "--property=ActiveState", "--value", "panel-deploy-"+interrupted.Identity.OperationID+".service")
		if state == "inactive" || state == "failed" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if result := wait(interrupted, "recovered"); result.ActiveReleaseID != first.Identity.ReleaseID {
		t.Fatal("interrupted execution lost the previous release")
	}
}

func nativeVMCommand(t *testing.T, ctx context.Context, name string, arguments ...string) string {
	t.Helper()
	command := exec.CommandContext(ctx, "/run/current-system/sw/bin/"+name, arguments...) // #nosec G204 -- fixed native fixture commands in a disposable VM.
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v: %s", name, err, data)
	}
	return strings.TrimSpace(string(data))
}
