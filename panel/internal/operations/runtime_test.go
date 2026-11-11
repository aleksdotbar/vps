package operations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/aleksdotbar/vps/panel/internal/store"
)

type effect struct {
	execute   func(context.Context, store.Operation) (Result, error)
	reconcile func(context.Context, store.Operation) (Result, error)
}

func (e effect) Execute(ctx context.Context, operation store.Operation) (Result, error) {
	return e.execute(ctx, operation)
}
func (e effect) Reconcile(ctx context.Context, operation store.Operation) (Result, error) {
	return e.reconcile(ctx, operation)
}
func openRuntime(t *testing.T, executor Executor) (*Runtime, *sql.DB) {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "state"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	runtime, err := New(context.Background(), database, executor, nil)
	if err != nil {
		t.Fatal(err)
	}
	return runtime, database
}
func deployment() (Intent, Assignment) {
	environmentID := uuid.NewString()
	return Intent{RequestID: uuid.NewString(), Kind: Deploy, EnvironmentID: environmentID, ExpectedVersion: 1, SourceSHA: strings.Repeat("a", 40)}, Assignment{ServerID: uuid.NewString(), EnvironmentID: environmentID, ReleaseID: uuid.NewString(), BindingVersion: 1}
}
func admit(t *testing.T, runtime *Runtime, intent Intent, target Assignment) store.Operation {
	t.Helper()
	tx, err := runtime.database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	operation, err := runtime.Enqueue(context.Background(), tx, intent, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return operation
}
func readOperation(t *testing.T, database *sql.DB, id string) store.Operation {
	t.Helper()
	operation, err := store.New(database).GetOperation(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return operation
}
func TestAdmissionRollbackDuplicateAndChangedIntent(t *testing.T) {
	ctx := context.Background()
	runtime, database := openRuntime(t, nil)
	intent, target := deployment()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	aborted, err := runtime.Enqueue(ctx, tx, intent, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.New(database).GetOperation(ctx, intent.RequestID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("aborted operation persisted: %v", err)
	}
	if _, err := runtime.client.JobGet(ctx, aborted.RiverJobID); err == nil {
		t.Fatal("aborted River job persisted")
	}
	original := admit(t, runtime, intent, target)
	changedAssignment := target
	changedAssignment.ReleaseID = uuid.NewString()
	duplicate := admit(t, runtime, intent, changedAssignment)
	if duplicate.ID != original.ID || duplicate.RiverJobID != original.RiverJobID || duplicate.ReleaseID != original.ReleaseID {
		t.Fatal("retry created a different intent/release/job")
	}
	job, err := runtime.client.JobGet(ctx, original.RiverJobID)
	if err != nil {
		t.Fatal(err)
	}
	var args map[string]string
	if err := json.Unmarshal(job.EncodedArgs, &args); err != nil {
		t.Fatal(err)
	}
	if len(args) != 1 || args["operation_id"] != original.ID {
		t.Fatal("job payload contains more than operation identity")
	}
	altered := intent
	altered.SourceSHA = strings.Repeat("b", 40)
	tx, err = database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := runtime.Enqueue(ctx, tx, altered, target); !errors.Is(err, ErrRequestReused) {
		t.Fatalf("changed intent accepted with same identity: %v", err)
	}
}
func TestAdmissionConflictsAndRollbackSupersedesOnlyQueuedAutomaticWork(t *testing.T) {
	ctx := context.Background()
	runtime, database := openRuntime(t, nil)
	intent, target := deployment()
	intent.Automatic = true
	automatic := admit(t, runtime, intent, target)
	conflicting := intent
	conflicting.RequestID = uuid.NewString()
	conflicting.Automatic = false
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Enqueue(ctx, tx, conflicting, target); !errors.Is(err, ErrConflict) {
		t.Fatalf("same environment overlap accepted: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	enrollment := Intent{RequestID: uuid.NewString(), Kind: Enroll, Enrollment: &EnrollmentInput{Name: "generic", Address: "generic.example.ts.net", Fingerprint: "SHA256:verified-out-of-band", User: "operator", BootstrapCredentialID: uuid.NewString()}}
	tx, err = database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Enqueue(ctx, tx, enrollment, Assignment{ServerID: target.ServerID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("host setup overlapped application work: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	rollback := Intent{RequestID: uuid.NewString(), Kind: Rollback, EnvironmentID: intent.EnvironmentID, ExpectedVersion: 1, TargetReleaseID: uuid.NewString()}
	rollbackTarget := target
	rollbackTarget.ReleaseID = rollback.TargetReleaseID
	replacement := admit(t, runtime, rollback, rollbackTarget)
	if readOperation(t, database, automatic.ID).State != "superseded" || replacement.State != "queued" {
		t.Fatal("rollback did not supersede queued automatic intent")
	}
	if err := runtime.work(ctx, replacement.ID, 1); err != nil {
		t.Fatal(err)
	}
	if readOperation(t, database, replacement.ID).State != "needs_attention" {
		t.Fatal("unsupported executor reported success")
	}
	tx, err = database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := runtime.Enqueue(ctx, tx, conflicting, target); !errors.Is(err, ErrConflict) {
		t.Fatalf("needs-attention environment was not blocked: %v", err)
	}
}
func TestRestartAfterStartReconcilesAndExhaustionStaysVisible(t *testing.T) {
	ctx := context.Background()
	executes, reconciles := 0, 0
	executor := effect{execute: func(context.Context, store.Operation) (Result, error) {
		executes++
		return Unknown, errors.New("opaque transport interruption")
	}, reconcile: func(context.Context, store.Operation) (Result, error) {
		reconciles++
		return Unknown, errors.New("opaque receipt unavailable")
	}}
	runtime, database := openRuntime(t, executor)
	intent, target := deployment()
	operation := admit(t, runtime, intent, target)
	if err := runtime.work(ctx, operation.ID, 1); err == nil {
		t.Fatal("unknown outcome treated as complete")
	}
	restarted, err := New(ctx, database, executor, nil)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 2; attempt <= MaxAttempts; attempt++ {
		_ = restarted.work(ctx, operation.ID, attempt)
	}
	final := readOperation(t, database, operation.ID)
	if executes != 1 || reconciles != 4 || !final.Started || final.State != "needs_attention" || final.Outcome != "unknown" || final.Attempts != 5 {
		t.Fatalf("unsafe restart/exhaustion: executes=%d reconciles=%d state=%s outcome=%s attempts=%d", executes, reconciles, final.State, final.Outcome, final.Attempts)
	}
	var snooze *rivertype.JobSnoozeError
	if err := restarted.work(ctx, operation.ID, 6); !errors.As(err, &snooze) {
		t.Fatal(err)
	}
	if executes != 1 || reconciles != 5 {
		t.Fatal("exhausted operation executed again")
	}
}
func TestStaleDeliveryCannotExecuteAgainOrOverwriteNewerReconciliation(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	executor := effect{execute: func(context.Context, store.Operation) (Result, error) {
		close(started)
		<-release
		return Succeeded, nil
	}, reconcile: func(context.Context, store.Operation) (Result, error) { return Unknown, nil }}
	runtime, database := openRuntime(t, executor)
	intent, target := deployment()
	operation := admit(t, runtime, intent, target)
	go func() { finished <- runtime.work(ctx, operation.ID, 1) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first execution did not start")
	}
	if err := runtime.work(ctx, operation.ID, 2); err == nil {
		t.Fatal("unknown reconciliation treated as success")
	}
	close(release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first execution did not return")
	}
	final := readOperation(t, database, operation.ID)
	if final.State != "recovering" || final.Outcome != "unknown" || final.Fence != 2 {
		t.Fatalf("stale result overrode reconciliation: %s/%s fence=%d", final.State, final.Outcome, final.Fence)
	}
}
func TestQueueCancellationCannotHideUnknownDomainOutcome(t *testing.T) {
	ctx := context.Background()
	runtime, database := openRuntime(t, effect{execute: func(context.Context, store.Operation) (Result, error) { return Unknown, nil }, reconcile: func(context.Context, store.Operation) (Result, error) { return Unknown, nil }})
	intent, target := deployment()
	operation := admit(t, runtime, intent, target)
	_ = runtime.work(ctx, operation.ID, 1)
	if _, err := runtime.client.JobCancel(ctx, operation.RiverJobID); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ReconcileState(ctx); err != nil {
		t.Fatal(err)
	}
	final := readOperation(t, database, operation.ID)
	if final.State != "needs_attention" || final.Outcome != "unknown" {
		t.Fatalf("cancelled queue job falsely resolved domain operation: %s/%s", final.State, final.Outcome)
	}
}
func TestActualRiverStartupProcessesOurAdmittedOperation(t *testing.T) {
	ctx := context.Background()
	runtime, database := openRuntime(t, effect{execute: func(context.Context, store.Operation) (Result, error) { return Succeeded, nil }, reconcile: func(context.Context, store.Operation) (Result, error) { return Succeeded, nil }})
	intent, target := deployment()
	operation := admit(t, runtime, intent, target)
	if err := runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bounded, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := runtime.Stop(bounded); err != nil {
			t.Error(err)
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if readOperation(t, database, operation.ID).State == "succeeded" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("admitted operation was not processed")
}
func TestWorkerPanicTextNeverReachesRiverErrorPayload(t *testing.T) {
	runtime, database := openRuntime(t, effect{execute: func(context.Context, store.Operation) (Result, error) { panic("sensitive provider content") }, reconcile: func(context.Context, store.Operation) (Result, error) { return Unknown, nil }})
	intent, target := deployment()
	operation := admit(t, runtime, intent, target)
	worker := operationWorker{runtime: runtime}
	err := worker.Work(context.Background(), &river.Job[jobArgs]{JobRow: &rivertype.JobRow{Attempt: 1}, Args: jobArgs{OperationID: operation.ID}})
	if err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("unsafe panic error: %v", err)
	}
	if !readOperation(t, database, operation.ID).Started {
		t.Fatal("panic lost started evidence")
	}
}

func TestLateConfirmedReceiptCanResolveAttentionWithoutNewExecution(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	runtime, database := openRuntime(t, effect{execute: func(context.Context, store.Operation) (Result, error) {
		close(started)
		<-release
		return Succeeded, nil
	}, reconcile: func(context.Context, store.Operation) (Result, error) { return Unknown, nil }})
	intent, target := deployment()
	operation := admit(t, runtime, intent, target)
	go func() { finished <- runtime.work(ctx, operation.ID, 1) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first execution did not start")
	}
	if _, err := runtime.client.JobCancel(ctx, operation.RiverJobID); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ReconcileState(ctx); err != nil {
		t.Fatal(err)
	}
	if readOperation(t, database, operation.ID).State != "needs_attention" {
		t.Fatal("lost job was not visible")
	}
	close(release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("confirmed receipt did not return")
	}
	if readOperation(t, database, operation.ID).State != "succeeded" {
		t.Fatal("same-fence confirmed receipt was ignored")
	}
}
func TestUnresolvedAdmissionCapacityRejectsNewIntent(t *testing.T) {
	runtime, database := openRuntime(t, nil)
	ctx := context.Background()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for index := 0; index < MaxUnresolved; index++ {
		intent, target := deployment()
		if _, err := runtime.Enqueue(ctx, tx, intent, target); err != nil {
			t.Fatalf("admission %d failed: %v", index, err)
		}
	}
	intent, target := deployment()
	if _, err := runtime.Enqueue(ctx, tx, intent, target); !errors.Is(err, ErrCapacity) {
		t.Fatalf("unbounded queue admission accepted: %v", err)
	}
}

func TestReconciliationNeverDowngradesKnownPartialOrUnknownOutcome(t *testing.T) {
	for _, test := range []struct {
		name     string
		first    Result
		expected string
	}{{"unknown", Unknown, "unknown"}, {"partial", Partial, "partial"}} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			runtime, database := openRuntime(t, effect{execute: func(context.Context, store.Operation) (Result, error) { return test.first, nil }, reconcile: func(context.Context, store.Operation) (Result, error) { return Unknown, nil }})
			intent, target := deployment()
			operation := admit(t, runtime, intent, target)
			_ = runtime.work(ctx, operation.ID, 1)
			if err := store.New(database).ReconcileStartedOperations(ctx, time.Now().UnixMilli()); err != nil {
				t.Fatal(err)
			}
			restarted, err := New(ctx, database, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := restarted.work(ctx, operation.ID, 2); err != nil {
				t.Fatal(err)
			}
			final := readOperation(t, database, operation.ID)
			if final.Outcome != test.expected || final.State != "needs_attention" {
				t.Fatalf("unsupported recovery erased %s evidence: %s/%s", test.expected, final.State, final.Outcome)
			}
		})
	}
}
func TestFinalPartialAttemptAndLostQueueRetainPartialEvidence(t *testing.T) {
	ctx := context.Background()
	runtime, database := openRuntime(t, effect{execute: func(context.Context, store.Operation) (Result, error) { return Partial, nil }, reconcile: func(context.Context, store.Operation) (Result, error) { return Partial, nil }})
	intent, target := deployment()
	operation := admit(t, runtime, intent, target)
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		_ = runtime.work(ctx, operation.ID, attempt)
	}
	final := readOperation(t, database, operation.ID)
	if final.State != "needs_attention" || final.Outcome != "partial" {
		t.Fatalf("final partial attempt lost evidence: %s/%s", final.State, final.Outcome)
	}
	// Native exhaustion also handles records whose River job ended independently.
	if _, err := database.ExecContext(ctx, "UPDATE operations SET state='recovering' WHERE id=?", operation.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.New(database).ExhaustOperation(ctx, store.ExhaustOperationParams{ID: operation.ID, UpdatedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if readOperation(t, database, operation.ID).Outcome != "partial" {
		t.Fatal("queue exhaustion erased partial evidence")
	}
}
func TestActualRiverRetryAfterRestartUsesPersistedStartedGuard(t *testing.T) {
	ctx := context.Background()
	var executes, reconciles atomic.Int32
	first, database := openRuntime(t, effect{execute: func(context.Context, store.Operation) (Result, error) {
		executes.Add(1)
		panic("simulated execution interruption")
	}, reconcile: func(context.Context, store.Operation) (Result, error) { return Unknown, nil }})
	intent, target := deployment()
	operation := admit(t, first, intent, target)
	events, unsubscribe := first.client.Subscribe(river.EventKindJobFailed)
	defer unsubscribe()
	if err := first.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bounded, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := first.Stop(bounded); err != nil {
			t.Error(err)
		}
	})
	select {
	case event := <-events:
		if event.Job.ID != operation.RiverJobID {
			t.Fatal("retry event identified another operation")
		}
		persistedJob, err := first.client.JobGet(ctx, operation.RiverJobID)
		if err != nil {
			t.Fatal(err)
		}
		if (persistedJob.State != rivertype.JobStateRetryable && persistedJob.State != rivertype.JobStateAvailable) || persistedJob.Attempt < 1 {
			t.Fatalf("operation did not persist retry: state=%s attempt=%d", persistedJob.State, persistedJob.Attempt)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first River attempt did not fail visibly")
	}
	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := first.Stop(stopCtx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	persisted := readOperation(t, database, operation.ID)
	if !persisted.Started || persisted.State != "running" {
		t.Fatal("first runtime lost interrupted execution evidence")
	}
	second, err := New(ctx, database, effect{execute: func(context.Context, store.Operation) (Result, error) { executes.Add(1); return Succeeded, nil }, reconcile: func(_ context.Context, operation store.Operation) (Result, error) {
		reconciles.Add(1)
		if !operation.Started {
			return Unknown, errors.New("started guard missing")
		}
		return Succeeded, nil
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bounded, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := second.Stop(bounded); err != nil {
			t.Error(err)
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if readOperation(t, database, operation.ID).State == "succeeded" {
			if executes.Load() != 1 || reconciles.Load() != 1 {
				t.Fatalf("restart repeated execution: executes=%d reconciles=%d", executes.Load(), reconciles.Load())
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("restarted River runtime did not reconcile retryable operation")
}
