package githubflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

func enableAutomatic(t *testing.T, f *buildFixture) {
	t.Helper()
	settings := proto.Clone(f.environment.Settings).(*panelv1.EnvironmentSettings)
	settings.AutoDeploy = true
	result, err := f.metadata.UpdateEnvironment(context.Background(), &panelv1.UpdateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Id: f.environment.Id, ExpectedVersion: f.environment.Version, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	f.environment = result.Environment
}
func discoveryDue(t *testing.T, f *buildFixture) {
	t.Helper()
	if _, err := f.service.database.ExecContext(context.Background(), "UPDATE github_branch_progress SET next_poll_at=0 WHERE environment_id=?", f.environment.Id); err != nil {
		t.Fatal(err)
	}
}
func discover(t *testing.T, f *buildFixture) {
	t.Helper()
	discoveryDue(t, f)
	if err := f.builds.Discover(context.Background(), f.environment.Id); err != nil {
		t.Fatal(err)
	}
}
func activeSHA(t *testing.T, f *buildFixture) (store.WaitingBuild, string) {
	t.Helper()
	waiting, err := store.New(f.service.database).ActiveWaitingBuildForEnvironment(context.Background(), f.environment.Id)
	if err != nil {
		return store.WaitingBuild{}, ""
	}
	var intent operations.Intent
	if err := json.Unmarshal(waiting.IntentJson, &intent); err != nil {
		t.Fatal(err)
	}
	return waiting, intent.SourceSHA
}
func TestAutomaticDiscoveryFollowsBranchHead(t *testing.T) {
	f := buildSetup(t)
	enableAutomatic(t, f)
	discover(t, f)
	first, sha := activeSHA(t, f)
	if sha != fixtureSHA || !first.Automatic {
		t.Fatalf("branch head not admitted: %q", sha)
	}
	discover(t, f)
	if again, _ := activeSHA(t, f); again.ID != first.ID {
		t.Fatal("unchanged head admitted twice")
	}
	next := strings.Repeat("b", 40)
	f.head.Store(next)
	discover(t, f)
	current, sha := activeSHA(t, f)
	if sha != next || current.ID == first.ID {
		t.Fatalf("new head did not supersede older automatic request: %q", sha)
	}
	previous, err := store.New(f.service.database).GetWaitingBuild(context.Background(), first.ID)
	if err != nil || previous.State != "superseded" || previous.Reason != "newer_build" {
		t.Fatalf("older automatic request survived: %v", err)
	}
}
func TestExplicitDeployMarksCurrentHeadHandled(t *testing.T) {
	f := buildSetup(t)
	ctx := context.Background()
	enableAutomatic(t, f)
	manual := f.request(t)
	if err := f.builds.finish(ctx, manual, "failed", "image_not_found"); err != nil {
		t.Fatal(err)
	}
	discover(t, f)
	if waiting, _ := activeSHA(t, f); waiting.ID != "" {
		t.Fatal("automatic deploy repeated the explicitly handled head")
	}
	next := strings.Repeat("c", 40)
	f.head.Store(next)
	discover(t, f)
	if _, sha := activeSHA(t, f); sha != next {
		t.Fatalf("newer push blocked after explicit deploy: %q", sha)
	}
}
func TestFailedBranchReadDefersWithoutAdmitting(t *testing.T) {
	f := buildSetup(t)
	ctx := context.Background()
	enableAutomatic(t, f)
	f.failBranch.Store(true)
	discover(t, f)
	progress, err := store.New(f.service.database).GetBranchProgress(ctx, f.environment.Id)
	if err != nil || progress.HandledSha != "" || progress.NextPollAt == 0 {
		t.Fatalf("failed read changed handled head or skipped deferral: %+v %v", progress, err)
	}
	if waiting, _ := activeSHA(t, f); waiting.ID != "" {
		t.Fatal("failed read admitted a deployment")
	}
}
func TestBindingResetForgetsHandledHead(t *testing.T) {
	f := buildSetup(t)
	ctx := context.Background()
	enableAutomatic(t, f)
	manual := f.request(t)
	if err := f.builds.finish(ctx, manual, "failed", "image_not_found"); err != nil {
		t.Fatal(err)
	}
	settings := proto.Clone(f.environment.Settings).(*panelv1.EnvironmentSettings)
	settings.Branch = "release"
	updated, err := f.metadata.UpdateEnvironment(ctx, &panelv1.UpdateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Id: f.environment.Id, ExpectedVersion: f.environment.Version, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	f.environment = updated.Environment
	discover(t, f)
	progress, err := store.New(f.service.database).GetBranchProgress(ctx, f.environment.Id)
	if err != nil || uint64(progress.BindingVersion) != f.environment.BindingVersion || progress.HandledSha != fixtureSHA {
		t.Fatalf("binding change kept the previous handled head: %+v %v", progress, err)
	}
	if _, sha := activeSHA(t, f); sha != fixtureSHA {
		t.Fatal("new binding's head was not deployed")
	}
}
func TestAutomaticDisableSupersedesCapturedRequestAcrossReenable(t *testing.T) {
	f := buildSetup(t)
	ctx := context.Background()
	enableAutomatic(t, f)
	discover(t, f)
	pending, _ := activeSHA(t, f)
	settings := proto.Clone(f.environment.Settings).(*panelv1.EnvironmentSettings)
	settings.AutoDeploy = false
	disabled, err := f.metadata.UpdateEnvironment(ctx, &panelv1.UpdateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Id: f.environment.Id, ExpectedVersion: f.environment.Version, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	settings.AutoDeploy = true
	if _, err := f.metadata.UpdateEnvironment(ctx, &panelv1.UpdateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Id: f.environment.Id, ExpectedVersion: disabled.Environment.Version, Settings: settings}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.builds.PollBuild(ctx, pending.ID); err != nil {
		t.Fatal(err)
	}
	pending, err = store.New(f.service.database).GetWaitingBuild(ctx, pending.ID)
	if err != nil || pending.State != "superseded" {
		t.Fatalf("disable/re-enable resurrected captured request: %v", err)
	}
	if f.sourceReads.Load() != 0 || f.images.calls.Load() != 0 {
		t.Fatal("superseded request read source or images")
	}
}

func TestSeededHandledHeadIsNotRedeployed(t *testing.T) {
	f := buildSetup(t)
	enableAutomatic(t, f)
	// Matches migration 00016: the running release's commit counts as handled.
	if _, err := f.service.database.ExecContext(context.Background(),
		"INSERT INTO github_branch_progress(environment_id,binding_version,handled_sha) SELECT id,binding_version,? FROM environments WHERE id=?",
		fixtureSHA, f.environment.Id); err != nil {
		t.Fatal(err)
	}
	discover(t, f)
	if waiting, _ := activeSHA(t, f); waiting.ID != "" {
		t.Fatal("handled branch head was admitted again")
	}
	next := strings.Repeat("b", 40)
	f.head.Store(next)
	discover(t, f)
	if _, sha := activeSHA(t, f); sha != next {
		t.Fatalf("newer head was not admitted: %q", sha)
	}
}
