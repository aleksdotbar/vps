package githubflow

import (
	"context"
	"testing"

	"github.com/google/go-github/v93/github"
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
	if err := store.New(f.service.database).DeferBranchPoll(context.Background(), store.DeferBranchPollParams{EnvironmentID: f.environment.Id, BindingVersion: 1, NextPollAt: 0}); err != nil {
		t.Fatal(err)
	}
}
func TestAutomaticDiscoveryKeepsNewestSuccessAndManualCutoff(t *testing.T) {
	f := buildSetup(t)
	ctx := context.Background()
	enableAutomatic(t, f)
	failed := fixtureRun(9, "push")
	failed.Conclusion = new("failure")
	f.latest = []*github.WorkflowRun{fixtureRun(5, "push"), failed, fixtureRun(7, "push")}
	if err := f.builds.Discover(ctx, f.environment.Id); err != nil {
		t.Fatal(err)
	}
	first, err := store.New(f.service.database).ActiveWaitingBuildForEnvironment(ctx, f.environment.Id)
	if err != nil || first.RunNumber != 7 {
		t.Fatalf("newest success not retained: %v", err)
	}
	discoveryDue(t, f)
	f.latest = []*github.WorkflowRun{fixtureRun(6, "push"), failed}
	if err := f.builds.Discover(ctx, f.environment.Id); err != nil {
		t.Fatal(err)
	}
	current, err := store.New(f.service.database).ActiveWaitingBuildForEnvironment(ctx, f.environment.Id)
	if err != nil || current.ID != first.ID {
		t.Fatalf("older/failed completion replaced success: %v", err)
	}
	discoveryDue(t, f)
	f.latest = []*github.WorkflowRun{fixtureRun(8, "push")}
	if err := f.builds.Discover(ctx, f.environment.Id); err != nil {
		t.Fatal(err)
	}
	current, err = store.New(f.service.database).ActiveWaitingBuildForEnvironment(ctx, f.environment.Id)
	if err != nil || current.RunNumber != 8 || current.ID == first.ID {
		t.Fatalf("newer success did not supersede queued request: %v", err)
	}
	previous, err := store.New(f.service.database).GetWaitingBuild(ctx, first.ID)
	if err != nil || previous.State != "superseded" {
		t.Fatalf("older automatic request survived: %v", err)
	}
	manual := f.request(t)
	if err := f.builds.finish(ctx, manual, "failed", "build_failed"); err != nil {
		t.Fatal(err)
	}
	discoveryDue(t, f)
	if err := f.builds.Discover(ctx, f.environment.Id); err != nil {
		t.Fatal(err)
	}
	capacity, err := store.New(f.service.database).WaitingBuildCapacity(ctx)
	if err != nil || capacity.Unresolved != 0 {
		t.Fatalf("old automatic build undid explicit intent: %v", err)
	}
	discoveryDue(t, f)
	f.latest = []*github.WorkflowRun{fixtureRun(10, "push")}
	if err := f.builds.Discover(ctx, f.environment.Id); err != nil {
		t.Fatal(err)
	}
	current, err = store.New(f.service.database).ActiveWaitingBuildForEnvironment(ctx, f.environment.Id)
	if err != nil || current.RunNumber != 10 {
		t.Fatalf("newer branch update blocked after manual cutoff: %v", err)
	}
}
func TestFailedProviderReadCannotRestoreCandidateClearedByConcurrentManualIntent(t *testing.T) {
	f := buildSetup(t)
	ctx := context.Background()
	enableAutomatic(t, f)
	queries := store.New(f.service.database)
	if err := queries.InitializeBranchProgress(ctx, store.InitializeBranchProgressParams{EnvironmentID: f.environment.Id, BindingVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if err := queries.SaveBranchProgress(ctx, store.SaveBranchProgressParams{EnvironmentID: f.environment.Id, DiscoveryPage: 1, CandidateRunID: 105, CandidateRunNumber: 5, CandidateAttempt: 1, CandidateSha: fixtureSHA}); err != nil {
		t.Fatal(err)
	}
	f.latest = []*github.WorkflowRun{fixtureRun(10, "push")}
	var manual string
	f.failDiscovery = func() { manual = f.request(t) }
	if err := f.builds.Discover(ctx, f.environment.Id); err != nil {
		t.Fatal(err)
	}
	progress, err := queries.GetBranchProgress(ctx, f.environment.Id)
	if err != nil || progress.CandidateRunID != 0 || progress.ManualCutoff != 10 {
		t.Fatalf("failed IO restored stale candidate/cutoff: %v", err)
	}
	f.failDiscovery = nil
	if err := f.builds.finish(ctx, manual, "failed", "build_failed"); err != nil {
		t.Fatal(err)
	}
	discoveryDue(t, f)
	f.latest = []*github.WorkflowRun{fixtureRun(5, "push")}
	if err := f.builds.Discover(ctx, f.environment.Id); err != nil {
		t.Fatal(err)
	}
	capacity, err := queries.WaitingBuildCapacity(ctx)
	if err != nil || capacity.Unresolved != 0 {
		t.Fatalf("stale candidate deployed after manual request finished: %v", err)
	}
}
func TestBindingResetDropsPreviousBranchManualCutoff(t *testing.T) {
	f := buildSetup(t)
	ctx := context.Background()
	enableAutomatic(t, f)
	queries := store.New(f.service.database)
	if err := queries.InitializeBranchProgress(ctx, store.InitializeBranchProgressParams{EnvironmentID: f.environment.Id, BindingVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if err := queries.SetManualBuildCutoff(ctx, store.SetManualBuildCutoffParams{EnvironmentID: f.environment.Id, ManualCutoff: 100, ManualAttempt: 1}); err != nil {
		t.Fatal(err)
	}
	settings := proto.Clone(f.environment.Settings).(*panelv1.EnvironmentSettings)
	settings.Branch = "release"
	updated, err := f.metadata.UpdateEnvironment(ctx, &panelv1.UpdateEnvironmentRequest{Mutation: &panelv1.Mutation{RequestId: uuid.NewString()}, Id: f.environment.Id, ExpectedVersion: f.environment.Version, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	f.environment = updated.Environment
	run := fixtureRun(5, "push")
	run.HeadBranch = new("release")
	f.latest = []*github.WorkflowRun{run}
	if err := f.builds.Discover(ctx, f.environment.Id); err != nil {
		t.Fatal(err)
	}
	progress, err := queries.GetBranchProgress(ctx, f.environment.Id)
	if err != nil || progress.BindingVersion != 2 || progress.ManualCutoff != 0 {
		t.Fatalf("previous binding cutoff retained: %v", err)
	}
	current, err := queries.ActiveWaitingBuildForEnvironment(ctx, f.environment.Id)
	if err != nil || current.RunNumber != 5 {
		t.Fatalf("new binding's older-numbered valid build suppressed: %v", err)
	}
}
func TestAutomaticDisableSupersedesCapturedRequestAcrossReenable(t *testing.T) {
	f := buildSetup(t)
	ctx := context.Background()
	enableAutomatic(t, f)
	f.latest = []*github.WorkflowRun{fixtureRun(7, "push")}
	if err := f.builds.Discover(ctx, f.environment.Id); err != nil {
		t.Fatal(err)
	}
	pending, err := store.New(f.service.database).ActiveWaitingBuildForEnvironment(ctx, f.environment.Id)
	if err != nil {
		t.Fatal(err)
	}
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
	if f.sourceReads.Load() != 0 || f.dispatches.Load() != 0 {
		t.Fatal("superseded request touched source/provider dispatch")
	}
}

func TestRollbackAdmissionClearsAlreadyPersistedAutomaticCandidate(t *testing.T) {
	f := buildSetup(t)
	ctx := context.Background()
	enableAutomatic(t, f)
	f.latest = []*github.WorkflowRun{fixtureRun(7, "push")}
	if err := f.builds.Discover(ctx, f.environment.Id); err != nil {
		t.Fatal(err)
	}
	waiting, err := store.New(f.service.database).ActiveWaitingBuildForEnvironment(ctx, f.environment.Id)
	if err != nil {
		t.Fatal(err)
	}
	if delay, err := f.builds.PollBuild(ctx, waiting.ID); err != nil || delay != 0 {
		t.Fatalf("automatic fixture release not admitted: %v", err)
	}
	operation, err := store.New(f.service.database).GetOperation(ctx, waiting.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.New(f.service.database).SaveBranchProgress(ctx, store.SaveBranchProgressParams{EnvironmentID: f.environment.Id, DiscoveryPage: 1, HighWaterRun: 7, HighWaterAttempt: 1, CandidateRunID: 108, CandidateAttempt: 1, CandidateRunNumber: 8, CandidateSha: fixtureSHA}); err != nil {
		t.Fatal(err)
	}
	tx, err := f.service.database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.runtime.Enqueue(ctx, tx, operations.Intent{RequestID: uuid.NewString(), Kind: operations.Rollback, EnvironmentID: f.environment.Id, ExpectedVersion: 3, TargetReleaseID: operation.ReleaseID}, operations.Assignment{ServerID: operation.ServerID, EnvironmentID: f.environment.Id, ReleaseID: operation.ReleaseID, BindingVersion: 1})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	progress, err := store.New(f.service.database).GetBranchProgress(ctx, f.environment.Id)
	if err != nil || progress.ManualCutoff != 8 || progress.ManualAttempt != 1 || progress.CandidateRunID != 0 {
		t.Fatalf("rollback retained stale candidate/order: %v", err)
	}
}
