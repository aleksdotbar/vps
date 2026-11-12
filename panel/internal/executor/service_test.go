package executor

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/aleksdotbar/vps/panel/internal/compose"
	"github.com/aleksdotbar/vps/panel/internal/deployment"
	"github.com/aleksdotbar/vps/panel/internal/hosthelper"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/releases"
	"github.com/aleksdotbar/vps/panel/internal/remote"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

type fixtureHelper func(context.Context, remote.Target, []byte, []byte) (remote.Result, error)

func (f fixtureHelper) Helper(ctx context.Context, target remote.Target, key, payload []byte) (remote.Result, error) {
	return f(ctx, target, key, payload)
}

func setupNativeInput(t *testing.T, deadline ...time.Time) *fixture {
	t.Helper()
	f := setup(t, deadline...)
	ctx := t.Context()
	queries := store.New(f.database)
	installation := uuid.NewString()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	owner := "ssh:" + installation + ":" + f.operation.ServerID
	if _, err := f.vault.SaveCredential(ctx, owner, 0, pem.EncodeToMemory(block)); err != nil {
		t.Fatal(err)
	}
	hostKey := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	fingerprint := ssh.FingerprintSHA256(signer.PublicKey())
	if _, err := f.database.ExecContext(ctx, `UPDATE servers SET ssh_fingerprint=?,panel_host=TRUE WHERE id=?`, fingerprint, f.operation.ServerID); err != nil {
		t.Fatal(err)
	}
	if err := queries.CreateServerManagement(ctx, store.CreateServerManagementParams{ServerID: f.operation.ServerID, CredentialKind: "credential", CredentialOwnerID: owner, CredentialVersion: 1, HostPublicKey: hostKey, ManagementFingerprint: fingerprint}); err != nil {
		t.Fatal(err)
	}
	if err := queries.CreateInstallation(ctx, store.CreateInstallationParams{InstallationID: installation, PanelServerID: f.operation.ServerID, BootstrapIdentity: bytes.Repeat([]byte{1}, 32), CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := queries.RecordPanelPrivateProof(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.vault.UpdateEnvironment(ctx, f.operation.EnvironmentID, 1, map[string]string{"API_TOKEN": "original-private-token", "UNDECLARED": "never-transfer"}, nil); err != nil {
		t.Fatal(err)
	}
	artifact := releases.Artifact{SchemaVersion: 1, SourceSHA: strings.Repeat("a", 40), Services: map[string]releases.ArtifactService{"app": {ImageVariable: "APP_IMAGE", Image: f.image}}}
	tree := t.TempDir()
	treeRoot, err := os.OpenRoot(tree)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = treeRoot.Close() }()
	source, err := os.ReadFile("../../../examples/stateless/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	source = bytes.Split(source, []byte("  worker:\n"))[0]
	if err := treeRoot.WriteFile("compose.yaml", source, 0600); err != nil {
		t.Fatal(err)
	}
	if err := treeRoot.WriteFile("compose.staging.yaml", []byte("services:\n  app:\n    environment:\n      MODE: staging\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command := os.Getenv("PANEL_TEST_COMPOSE")
	if command == "" {
		command = "docker-compose"
	}
	resolver, err := compose.New(command)
	if err != nil {
		t.Fatal(err)
	}
	bridge := "vps-1234567890"
	prepared, err := resolver.Resolve(ctx, tree, "compose.staging.yaml", artifact, map[string]bool{"API_TOKEN": true}, compose.Network{ProjectName: "vps-" + strings.ReplaceAll(f.operation.EnvironmentID, "-", ""), BridgeName: bridge})
	if err != nil {
		t.Fatal(err)
	}
	imageData := []byte(`[{"service":"app","image_variable":"APP_IMAGE","image_digest":"` + f.image + `"}]`)
	// #nosec G117 -- Binding metadata contains variable names, never secret values.
	bindings, err := json.Marshal(prepared.Bindings)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := releases.SnapshotSHA256(prepared.Config, []byte(`[]`), f.operation.EnvironmentID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.ExecContext(ctx, `UPDATE releases SET images_json=?,compose_config=?,secret_bindings_json=?,secret_version=2,config_sha256=? WHERE id=?`, imageData, prepared.Config, bindings, hash, f.candidate); err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.ExecContext(ctx, `UPDATE environments SET bridge_name=? WHERE id=?`, bridge, f.operation.EnvironmentID); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestLostStartReconcilesWithoutRestagingSecretsAndRejectsReplayedProof(t *testing.T) {
	f := setupNativeInput(t)
	ctx := t.Context()
	queries := store.New(f.database)
	if _, err := f.vault.SaveCredential(ctx, "ghcr-read", 0, []byte("original-registry-token")); err != nil {
		t.Fatal(err)
	}
	if err := queries.SetRegistryCredential(ctx, store.SetRegistryCredentialParams{Username: "operator", Version: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(ctx, f.database, f.operation); err != nil {
		t.Fatal(err)
	}
	if _, err := f.vault.SaveCredential(ctx, "ghcr-read", 1, []byte("edited-registry-token")); err != nil {
		t.Fatal(err)
	}
	if err := queries.SetRegistryCredential(ctx, store.SetRegistryCredentialParams{Username: "edited", Version: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.vault.UpdateEnvironment(ctx, f.operation.EnvironmentID, 2, map[string]string{"API_TOKEN": "edited-private-token"}, nil); err != nil {
		t.Fatal(err)
	}
	service := New(f.database, f.cipher, nil, nil)
	stages, starts, observations := 0, 0, 0
	finalSystem := "/nix/store/" + strings.Repeat("a", 32) + "-nixos-system-fixture"
	service.remote = fixtureHelper(func(_ context.Context, _ remote.Target, _ []byte, payload []byte) (remote.Result, error) {
		var response any
		if request, err := hosthelper.ParseReadinessRequest(payload); err == nil {
			response = hosthelper.ReadinessResponse{SchemaVersion: 1, Action: "readiness", InstallationID: request.InstallationID, ServerID: request.ServerID, Challenge: request.Challenge, Role: "panel", CurrentSystem: finalSystem, SystemProfile: finalSystem, BootedSystem: finalSystem, Status: "ready", DockerVersion: hosthelper.DockerVersion, ComposeVersion: hosthelper.ComposeVersion}
		} else if request, err := deployment.ParseStage(payload); err == nil {
			defer request.Clear()
			stages++
			if len(request.SecretValues) != 1 || string(request.SecretValues["API_TOKEN"]) != "original-private-token" || request.Registry == nil || request.Registry.Username != "operator" || string(request.Registry.Token) != "original-registry-token" {
				t.Fatal("staging changed captured versions or leaked undeclared secrets")
			}
			if bytes.Contains(payload, []byte("never-transfer")) || bytes.Contains(payload, []byte("edited-private-token")) {
				t.Fatal("unexpected secret crossed SSH boundary")
			}
			hash, err := deployment.ManifestSHA256(request.Manifest)
			if err != nil {
				t.Fatal(err)
			}
			response = deployment.Response{SchemaVersion: 1, Action: request.Action, Challenge: request.Challenge, Identity: request.Manifest.Identity, ManifestSHA256: hash, Phase: "staged", FinalSystem: finalSystem}
		} else {
			request, err := deployment.ParseExecution(payload)
			if err != nil {
				t.Fatal(err)
			}
			if request.Action == "deploy_start" {
				starts++
				return remote.Result{}, remote.ErrTransport
			}
			if request.Action != "deploy_observe" {
				t.Fatalf("unexpected reconciliation action %s", request.Action)
			}
			observations++
			nonce := request.Challenge
			if observations == 1 {
				nonce = strings.Repeat("f", 64)
			}
			response = deployment.ExecutionResponse{SchemaVersion: 1, Action: request.Action, Challenge: nonce, Identity: request.Identity, ManifestSHA256: request.ManifestSHA256, FinalSystem: finalSystem, Phase: "succeeded", ActiveReleaseID: f.candidate, Services: []deployment.ServiceObservation{{Service: "app", Image: f.image, ContainerID: strings.Repeat("b", 64), State: "running", Health: "healthy"}}}
		}
		data, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		return remote.Result{Outcome: remote.CommandExited, Output: data}, nil
	})
	if result, err := service.Execute(ctx, f.operation); err == nil || result != operations.Unknown {
		t.Fatalf("lost start did not remain unknown: %v %v", result, err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		operation, err := queries.ClaimOperation(ctx, store.ClaimOperationParams{ID: f.operation.ID, UpdatedAt: time.Now().UnixMilli(), ExecutionDeadline: time.Now().Add(time.Hour).UnixMilli()})
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.Reconcile(ctx, operation)
		environment, readErr := queries.GetEnvironment(ctx, f.operation.EnvironmentID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if attempt == 0 {
			if err == nil || result != operations.Unknown || environment.CurrentReleaseID.String != f.previous {
				t.Fatal("forged observation promoted release")
			}
		} else if err != nil || result != operations.Succeeded || environment.CurrentReleaseID.String != f.candidate {
			t.Fatalf("verified terminal result did not reconcile: %v %v", result, err)
		}
	}
	if stages != 1 || starts != 1 || observations != 2 {
		t.Fatalf("remote effect replayed: stages=%d starts=%d observations=%d", stages, starts, observations)
	}
}

func TestTerminalObservationNeverStagesOrConfirmsAfterEffectsStop(t *testing.T) {
	finalSystem := "/nix/store/" + strings.Repeat("a", 32) + "-nixos-system-fixture"
	for _, name := range []string{"expired", "needs_attention"} {
		t.Run(name, func(t *testing.T) {
			deadline := time.Now().Add(time.Minute)
			if name == "expired" {
				deadline = time.Now().Add(-time.Minute)
			}
			f := setupNativeInput(t, deadline)
			ctx := t.Context()
			queries := store.New(f.database)
			if err := queries.CaptureDeploymentExecution(ctx, store.CaptureDeploymentExecutionParams{OperationID: f.operation.ID, PreviousReleaseID: sql.NullString{String: f.previous, Valid: true}, CreatedAt: 1}); err != nil {
				t.Fatal(err)
			}
			if name == "needs_attention" {
				if _, err := queries.FinishOperation(ctx, store.FinishOperationParams{State: "needs_attention", Outcome: "unknown", Reason: "reconciliation_exhausted", UpdatedAt: 1, ID: f.operation.ID, Fence: f.operation.Fence}); err != nil {
					t.Fatal(err)
				}
				current, err := queries.GetOperation(ctx, f.operation.ID)
				if err != nil {
					t.Fatal(err)
				}
				f.operation = current
			}
			phase := "awaiting_dns"
			observations := 0
			service := New(f.database, f.cipher, nil, nil)
			service.remote = fixtureHelper(func(ctx context.Context, _ remote.Target, _, payload []byte) (remote.Result, error) {
				if ctx.Err() != nil {
					t.Fatal("read-only observation was already expired")
				}
				var response any
				if request, err := hosthelper.ParseReadinessRequest(payload); err == nil {
					response = hosthelper.ReadinessResponse{SchemaVersion: 1, Action: "readiness", InstallationID: request.InstallationID, ServerID: request.ServerID, Challenge: request.Challenge, Role: "panel", CurrentSystem: finalSystem, SystemProfile: finalSystem, BootedSystem: finalSystem, Status: "ready", DockerVersion: hosthelper.DockerVersion, ComposeVersion: hosthelper.ComposeVersion}
				} else {
					request, err := deployment.ParseExecution(payload)
					if err != nil || request.Action != "deploy_observe" {
						t.Fatal("read-only settlement attempted a target mutation")
					}
					observations++
					response = deployment.ExecutionResponse{SchemaVersion: 1, Action: request.Action, Challenge: request.Challenge, Identity: request.Identity, ManifestSHA256: request.ManifestSHA256, FinalSystem: finalSystem, Phase: phase, ActiveReleaseID: f.candidate, Services: []deployment.ServiceObservation{{Service: "app", Image: f.image, ContainerID: strings.Repeat("b", 64), State: "running", Health: "healthy"}}}
				}
				data, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				return remote.Result{Outcome: remote.CommandExited, Output: data}, nil
			})
			if result, err := service.Reconcile(ctx, f.operation); result != operations.Unknown || err != nil {
				t.Fatalf("unconfirmed release changed outcome: %v %v", result, err)
			}
			phase = "succeeded"
			if result, err := service.Reconcile(ctx, f.operation); result != operations.Succeeded || err != nil {
				t.Fatalf("terminal proof did not settle: %v %v", result, err)
			}
			current, err := queries.GetOperation(ctx, f.operation.ID)
			if err != nil || observations != 2 || current.State != "succeeded" || current.ExecutionDeadline != deadline.UnixMilli() {
				t.Fatal("settlement changed operation identity or deadline")
			}
		})
	}
}
