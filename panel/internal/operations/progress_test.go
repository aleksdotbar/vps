package operations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/riverqueue/river/rivertype"

	"github.com/aleksdotbar/vps/panel/internal/store"
)

func TestVerifiedProgressPreservesDeadlineAndFailureBudgetAcrossRestart(t *testing.T) {
	ctx := context.Background()
	executes, reconciles := 0, 0
	observation := Running
	executor := effect{execute: func(context.Context, store.Operation) (Result, error) {
		executes++
		return observation, nil
	}, reconcile: func(context.Context, store.Operation) (Result, error) {
		reconciles++
		return observation, nil
	}}
	runtime, database := openRuntime(t, executor)
	intent, target := deployment()
	op := admit(t, runtime, intent, target)
	var deadline int64
	for poll := 0; poll < 8; poll++ {
		if poll == 3 {
			var err error
			runtime, err = New(ctx, database, executor, nil)
			if err != nil {
				t.Fatal(err)
			}
		}
		if poll == 4 {
			observation = Recovering
		}
		if poll == 5 {
			observation = Running
		}
		var snooze *rivertype.JobSnoozeError
		if err := runtime.work(ctx, op.ID, 1); !errors.As(err, &snooze) {
			t.Fatalf("poll %d did not schedule observation: %v", poll, err)
		}
		current := readOperation(t, database, op.ID)
		if poll == 0 {
			deadline = current.ExecutionDeadline
		}
		if deadline <= time.Now().UnixMilli() || current.ExecutionDeadline != deadline || current.Attempts != 0 || current.Fence != int64(poll+1) {
			t.Fatalf("poll %d changed lifetime/budget: deadline=%d attempts=%d fence=%d", poll, current.ExecutionDeadline, current.Attempts, current.Fence)
		}
		if poll >= 4 && (current.State != "recovering" || current.Outcome != "partial") {
			t.Fatalf("poll %d lost recovery evidence: %s/%s", poll, current.State, current.Outcome)
		}
	}
	observation = Unknown
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		_ = runtime.work(ctx, op.ID, attempt)
	}
	final := readOperation(t, database, op.ID)
	if executes != 1 || reconciles != 12 || final.Attempts != MaxAttempts || final.State != "needs_attention" || final.Outcome != "partial" || final.ExecutionDeadline != deadline {
		t.Fatalf("incorrect failure exhaustion after progress: executes=%d reconciles=%d state=%s outcome=%s attempts=%d", executes, reconciles, final.State, final.Outcome, final.Attempts)
	}
	if err := runtime.observeProgress(ctx, final, Running); err != nil {
		t.Fatal(err)
	}
	if current := readOperation(t, database, op.ID); current.State != "needs_attention" || current.Attempts != MaxAttempts {
		t.Fatal("late progress revived exhausted operation or refunded its failures")
	}
}

func TestExpiredOperationOnlyObservesAndNeverRefreshesDeadline(t *testing.T) {
	for _, monitor := range []bool{false, true} {
		t.Run(map[bool]string{false: "worker", true: "monitor"}[monitor], func(t *testing.T) {
			ctx := context.Background()
			unexpected := func(context.Context, store.Operation) (Result, error) {
				t.Fatal("expired operation invoked executor")
				return Unknown, nil
			}
			runtime, database := openRuntime(t, effect{execute: unexpected, reconcile: func(context.Context, store.Operation) (Result, error) { return Unknown, nil }})
			intent, target := deployment()
			op := admit(t, runtime, intent, target)
			deadline := time.Now().Add(-time.Minute).UnixMilli()
			queries := store.New(database)
			claimed, err := queries.ClaimOperation(ctx, store.ClaimOperationParams{ID: op.ID, UpdatedAt: deadline, ExecutionDeadline: deadline})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.ExecContext(ctx, "UPDATE operations SET outcome='partial' WHERE id=?", op.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := database.ExecContext(ctx, "UPDATE operations SET execution_deadline=? WHERE id=?", deadline+60000, op.ID); err == nil {
				t.Fatal("persisted deadline was mutable")
			}
			if monitor {
				err = runtime.ReconcileState(ctx)
			} else {
				err = runtime.work(ctx, op.ID, 2)
			}
			var snooze *rivertype.JobSnoozeError
			if err != nil && !errors.As(err, &snooze) {
				t.Fatal(err)
			}
			final := readOperation(t, database, op.ID)
			if final.State != "needs_attention" || final.Outcome != "partial" || final.Reason != "execution_deadline_exceeded" || final.ExecutionDeadline != deadline || final.Fence != claimed.Fence {
				t.Fatalf("expiry lost state: %s/%s/%s", final.State, final.Outcome, final.Reason)
			}
			if err := runtime.observeProgress(ctx, claimed, Running); err != nil {
				t.Fatal(err)
			}
			if readOperation(t, database, op.ID).State != "needs_attention" {
				t.Fatal("late progress revived expired operation")
			}
		})
	}
}

func TestRetryExhaustionCannotOverrideNewerProgressRefund(t *testing.T) {
	ctx := context.Background()
	runtime, database := openRuntime(t, nil)
	intent, target := deployment()
	op := admit(t, runtime, intent, target)
	queries := store.New(database)
	var claimed store.Operation
	for attempt := 0; attempt < MaxAttempts; attempt++ {
		var err error
		claimed, err = queries.ClaimOperation(ctx, store.ClaimOperationParams{ID: op.ID, UpdatedAt: time.Now().UnixMilli(), ExecutionDeadline: time.Now().Add(MaxExecutionDuration).UnixMilli()})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Monitor holds this fifth-attempt snapshot when the verified receipt arrives.
	var snooze *rivertype.JobSnoozeError
	if err := runtime.observeProgress(ctx, claimed, Running); !errors.As(err, &snooze) {
		t.Fatal(err)
	}
	changed, err := queries.ExhaustOperationRetries(ctx, store.ExhaustOperationRetriesParams{ID: claimed.ID, UpdatedAt: time.Now().UnixMilli()})
	if err != nil || changed != 0 {
		t.Fatalf("stale snapshot exhausted progress: changed=%d error=%v", changed, err)
	}
	current := readOperation(t, database, op.ID)
	if current.State != "running" || current.Attempts != MaxAttempts-1 {
		t.Fatalf("progress lost: state=%s attempts=%d", current.State, current.Attempts)
	}
}

func TestLateTerminalObservationUsesExistingJobWithoutNewEffects(t *testing.T) {
	ctx := t.Context()
	reconciles := 0
	runtime, database := openRuntime(t, effect{
		execute: func(context.Context, store.Operation) (Result, error) {
			t.Fatal("late observation executed deployment")
			return Unknown, nil
		},
		reconcile: func(ctx context.Context, _ store.Operation) (Result, error) {
			reconciles++
			deadline, ok := ctx.Deadline()
			if !ok || !deadline.After(time.Now()) {
				t.Fatal("observation inherited expired effect deadline")
			}
			return Succeeded, nil
		},
	})
	intent, target := deployment()
	op := admit(t, runtime, intent, target)
	deadline := time.Now().Add(-time.Minute).UnixMilli()
	claimed, err := store.New(database).ClaimOperation(ctx, store.ClaimOperationParams{ID: op.ID, UpdatedAt: deadline, ExecutionDeadline: deadline})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.New(database).ExpireOperation(ctx, store.ExpireOperationParams{ID: op.ID, Now: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.client.JobCancel(ctx, op.RiverJobID); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ReconcileState(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := runtime.client.JobGet(ctx, op.RiverJobID)
	if err != nil || job.State != rivertype.JobStateAvailable {
		t.Fatalf("terminal observation not scheduled: %v", err)
	}
	if err := runtime.work(ctx, op.ID, MaxAttempts); err != nil {
		t.Fatal(err)
	}
	result := readOperation(t, database, op.ID)
	if reconciles != 1 || result.State != "succeeded" || result.ExecutionDeadline != deadline || result.Fence != claimed.Fence {
		t.Fatal("late receipt did not settle the original intent")
	}
}
