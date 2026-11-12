package executor

import (
	"bytes"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/projects"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
	"github.com/google/uuid"
)

type fixture struct {
	database                   *sql.DB
	cipher                     *securestate.Cipher
	vault                      *securestate.Vault
	operation                  store.Operation
	previous, candidate, image string
}

func setup(t *testing.T, deadlines ...time.Time) *fixture {
	t.Helper()
	ctx := t.Context()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "state"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runtime, err := operations.New(ctx, database, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := securestate.NewCipher(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{database: database, cipher: cipher, vault: securestate.NewVault(database, cipher), previous: uuid.NewString(), candidate: uuid.NewString(), image: "ghcr.io/example/app@sha256:" + strings.Repeat("a", 64)}
	queries := store.New(database)
	server, project, environment := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if err := queries.CreateServer(ctx, store.CreateServerParams{ID: server, Name: "fixture", TailscaleAddress: "100.64.0.1", SshFingerprint: "fixture", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := queries.ObserveServer(ctx, store.ObserveServerParams{ID: server, Readiness: "ready", ObservedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := queries.CreateProject(ctx, store.CreateProjectParams{ID: project, Name: "fixture", RepositoryID: 1, RepositoryOwner: "example", RepositoryName: "app", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.vault.UpdateEnvironment(ctx, environment, 0, map[string]string{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := queries.CreateEnvironment(ctx, store.CreateEnvironmentParams{ID: environment, ProjectID: project, Name: "staging", ServerID: server, Branch: "main", OverridePath: "compose.staging.yaml", DomainsJson: []byte(`[]`), SecretNamesJson: []byte(`[]`), BridgeName: "fixture", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{f.previous, f.candidate} {
		if err := queries.CreateRelease(ctx, store.CreateReleaseParams{ID: id, EnvironmentID: environment, ServerID: server, RepositoryID: 1, Branch: "main", SourceSha: strings.Repeat("a", 40), EnvironmentVersion: int64(i + 1), BindingVersion: 1, ImagesJson: []byte(`[]`), DomainsJson: []byte(`[]`), SecretVersion: 1, ComposeConfig: []byte(`{}`), SecretBindingsJson: []byte(`[]`), ConfigSha256: strings.Repeat("a", 64), CreatedAt: int64(i + 1)}); err != nil {
			t.Fatal(err)
		}
		if err := queries.CreateReleaseService(ctx, store.CreateReleaseServiceParams{ReleaseID: id, Name: "app", ImageDigest: f.image}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.ExecContext(ctx, `UPDATE environments SET current_release_id=?,applied_version=1,version=3 WHERE id=?`, f.previous, environment); err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	operation, err := runtime.Enqueue(ctx, tx, operations.Intent{RequestID: uuid.NewString(), Kind: operations.Deploy, EnvironmentID: environment, ExpectedVersion: 3, SourceSHA: strings.Repeat("a", 40)}, operations.Assignment{ServerID: server, EnvironmentID: environment, ReleaseID: f.candidate, BindingVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Minute)
	if len(deadlines) > 0 {
		deadline = deadlines[0]
	}
	f.operation, err = queries.ClaimOperation(ctx, store.ClaimOperationParams{ID: operation.ID, UpdatedAt: time.Now().UnixMilli(), ExecutionDeadline: deadline.UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) observed(result operations.Result) completion {
	active := f.candidate
	if result == operations.Recovered {
		active = f.previous
	}
	return completion{Result: result, ActiveReleaseID: active, Services: []observedService{{Name: "app", Image: f.image, ContainerID: strings.Repeat("b", 64)}}}
}

func TestCaptureRetainsRegistryVersionAndBaselineAcrossRetry(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	queries := store.New(f.database)
	for _, version := range []int64{1, 2} {
		if _, err := f.vault.SaveCredential(ctx, "ghcr-read", version-1, []byte("private-registry-token")); err != nil {
			t.Fatal(err)
		}
		if err := queries.SetRegistryCredential(ctx, store.SetRegistryCredentialParams{Username: "operator", Version: version}); err != nil {
			t.Fatal(err)
		}
		captured, err := capture(ctx, f.database, f.operation)
		if err != nil {
			t.Fatal(err)
		}
		if captured.RegistryVersion.Int64 != 1 || captured.RegistryUsername != "operator" || captured.PreviousReleaseID.String != f.previous {
			t.Fatalf("retry changed captured inputs: %+v", captured)
		}
	}
	stale := f.operation
	stale.Fence++
	if _, err := capture(ctx, f.database, stale); !errors.Is(err, errExecutionState) {
		t.Fatalf("stale fence accepted: %v", err)
	}
}

func TestCompletionIsAtomicAndRetainsPendingChanges(t *testing.T) {
	for _, result := range []operations.Result{operations.Succeeded, operations.Recovered} {
		state := map[operations.Result]string{operations.Succeeded: "succeeded", operations.Recovered: "recovered"}[result]
		t.Run(state, func(t *testing.T) {
			f := setup(t)
			ctx := t.Context()
			queries := store.New(f.database)
			if _, err := capture(ctx, f.database, f.operation); err != nil {
				t.Fatal(err)
			}
			// Fail the last write, after release promotion and service observations.
			if _, err := f.database.ExecContext(ctx, `CREATE TRIGGER fail_finish BEFORE UPDATE OF state ON operations BEGIN SELECT RAISE(ABORT,'fixture finish failure'); END`); err != nil {
				t.Fatal(err)
			}
			if err := complete(ctx, f.database, f.operation, f.observed(result)); err == nil {
				t.Fatal("injected final-write failure accepted")
			}
			env, err := queries.GetEnvironment(ctx, f.operation.EnvironmentID)
			if err != nil {
				t.Fatal(err)
			}
			op, err := queries.GetOperation(ctx, f.operation.ID)
			if err != nil {
				t.Fatal(err)
			}
			var count int
			if err := f.database.QueryRowContext(ctx, `SELECT count(*) FROM service_observations`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if env.CurrentReleaseID.String != f.previous || env.AppliedVersion != 1 || op.State != "running" || count != 0 {
				t.Fatal("partial completion persisted")
			}
			if _, err := f.database.ExecContext(ctx, `DROP TRIGGER fail_finish`); err != nil {
				t.Fatal(err)
			}
			if err := complete(ctx, f.database, f.operation, f.observed(result)); err != nil {
				t.Fatal(err)
			}
			env, err = queries.GetEnvironment(ctx, f.operation.EnvironmentID)
			if err != nil {
				t.Fatal(err)
			}
			op, err = queries.GetOperation(ctx, f.operation.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantRelease, wantVersion := f.candidate, int64(2)
			if result == operations.Recovered {
				wantRelease, wantVersion = f.previous, 1
			}
			if env.CurrentReleaseID.String != wantRelease || env.AppliedVersion != wantVersion || env.Version != 3 || op.State != state {
				t.Fatal("wrong committed release, pending version or outcome")
			}
			if result == operations.Succeeded && env.PreviousReleaseID.String != f.previous {
				t.Fatal("lost rollback reference")
			}
			// An old completion retry cannot overwrite a later current release.
			if _, err := f.database.ExecContext(ctx, `UPDATE environments SET current_release_id=? WHERE id=?`, f.previous, env.ID); err != nil {
				t.Fatal(err)
			}
			if err := complete(ctx, f.database, f.operation, f.observed(result)); err != nil {
				t.Fatal(err)
			}
			env, err = queries.GetEnvironment(ctx, env.ID)
			if err != nil || env.CurrentReleaseID.String != f.previous {
				t.Fatalf("late retry rewrote current release: %v", err)
			}
		})
	}
}

func TestCompletionRejectsMismatchedObservationAndExposesOnlyFreshState(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	if _, err := capture(ctx, f.database, f.operation); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"image", "missing", "duplicate", "container", "release", "fence"} {
		observation, operation := f.observed(operations.Succeeded), f.operation
		switch mode {
		case "image":
			observation.Services[0].Image = "ghcr.io/example/other@sha256:" + strings.Repeat("a", 64)
		case "missing":
			observation.Services = nil
		case "duplicate":
			observation.Services = append(observation.Services, observation.Services[0])
		case "container":
			observation.Services[0].ContainerID = strings.Repeat("B", 64)
		case "release":
			observation.ActiveReleaseID = f.previous
		case "fence":
			operation.Fence++
		}
		if err := complete(ctx, f.database, operation, observation); err == nil {
			t.Fatalf("accepted %s mismatch", mode)
		}
	}
	if err := complete(ctx, f.database, f.operation, f.observed(operations.Succeeded)); err != nil {
		t.Fatal(err)
	}
	service := projects.New(f.database, f.cipher)
	response, err := service.ListServices(ctx, f.operation.EnvironmentID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Services) != 1 || response.Services[0].State != panelv1.ServiceState_SERVICE_STATE_RUNNING || response.Services[0].ObservedReleaseId != f.candidate {
		t.Fatal("verified current service not exposed")
	}
	if err := store.New(f.database).ObserveServer(ctx, store.ObserveServerParams{ID: f.operation.ServerID, Readiness: "unreachable", ObservedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	response, err = service.ListServices(ctx, f.operation.EnvironmentID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Services[0].State != panelv1.ServiceState_SERVICE_STATE_UNKNOWN {
		t.Fatal("unreachable host retained healthy service claim")
	}
	if err := store.New(f.database).ObserveServer(ctx, store.ObserveServerParams{ID: f.operation.ServerID, Readiness: "ready", ObservedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.ExecContext(ctx, `UPDATE operations SET state='recovering',outcome='unknown' WHERE id=?`, f.operation.ID); err != nil {
		t.Fatal(err)
	}
	response, err = service.ListServices(ctx, f.operation.EnvironmentID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Services[0].State != panelv1.ServiceState_SERVICE_STATE_UNKNOWN {
		t.Fatal("started replacement retained healthy current-state claim")
	}
	if _, err := f.database.ExecContext(ctx, `UPDATE operations SET state='succeeded',outcome='success' WHERE id=?`, f.operation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.ExecContext(ctx, `UPDATE service_observations SET observed_at=1`); err != nil {
		t.Fatal(err)
	}
	response, err = service.ListServices(ctx, f.operation.EnvironmentID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Services[0].State != panelv1.ServiceState_SERVICE_STATE_UNKNOWN || response.Services[0].Health != panelv1.ServiceHealth_SERVICE_HEALTH_UNKNOWN || response.Services[0].ObservedAt == nil {
		t.Fatal("stale observation represented as current health or lost timestamp")
	}
}

func TestCompletionHandlesFirstDeployAndSameReleaseWithoutLosingRollback(t *testing.T) {
	for _, mode := range []string{"first deployment", "same release"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			ctx := t.Context()
			if mode == "first deployment" {
				if _, err := f.database.ExecContext(ctx, `UPDATE environments SET current_release_id=NULL,applied_version=0 WHERE id=?`, f.operation.EnvironmentID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.database.ExecContext(ctx, `UPDATE environments SET current_release_id=?,previous_release_id=? WHERE id=?`, f.candidate, f.previous, f.operation.EnvironmentID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := capture(ctx, f.database, f.operation); err != nil {
				t.Fatal(err)
			}
			if mode == "first deployment" {
				if err := complete(ctx, f.database, f.operation, f.observed(operations.Recovered)); !errors.Is(err, errObservation) {
					t.Fatalf("first deploy claimed recovery without previous release: %v", err)
				}
			}
			if err := complete(ctx, f.database, f.operation, f.observed(operations.Succeeded)); err != nil {
				t.Fatal(err)
			}
			env, err := store.New(f.database).GetEnvironment(ctx, f.operation.EnvironmentID)
			if err != nil {
				t.Fatal(err)
			}
			if env.CurrentReleaseID.String != f.candidate {
				t.Fatal("candidate was not promoted")
			}
			if mode == "same release" && env.PreviousReleaseID.String != f.previous {
				t.Fatal("same-release deployment lost previous rollback release")
			}
			if mode == "first deployment" && env.PreviousReleaseID.Valid {
				t.Fatal("invented previous release")
			}
		})
	}
}
