package operations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/riverqueue/river"

	"github.com/aleksdotbar/vps/panel/internal/store"
)

type jobArgs struct {
	OperationID string `json:"operation_id"`
}

const MaxExecutionDuration = 30 * time.Minute
const progressPollInterval = 15 * time.Second

func (jobArgs) Kind() string { return "environment_operation_v1" }

type operationWorker struct {
	river.WorkerDefaults[jobArgs]
	runtime *Runtime
}

func (w *operationWorker) NextRetry(job *river.Job[jobArgs]) time.Time {
	// Five attempts, with bounded 1/2/4/8/16-second retry intervals.
	attempt := job.Attempt
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 5 {
		attempt = 5
	}
	return time.Now().Add(time.Second * time.Duration(1<<uint(attempt-1)))
}
func (w *operationWorker) Work(ctx context.Context, job *river.Job[jobArgs]) (returnErr error) {
	// Executor panics may contain secrets; never hand their text to River logging
	// or error payloads. Persisted started=true ensures the next attempt reconciles.
	defer func() {
		if recover() != nil {
			returnErr = errors.New("operation execution interrupted")
		}
	}()
	return w.runtime.work(ctx, job.Args.OperationID, job.Attempt)
}
func (r *Runtime) work(ctx context.Context, id string, riverAttempt int) error {
	previous, err := store.New(r.database).GetOperation(ctx, id)
	if err != nil {
		return fmt.Errorf("load operation identity: %w", err)
	}
	if previous.State != "queued" && previous.State != "running" && previous.State != "recovering" && previous.State != "needs_attention" {
		return nil
	}
	now := time.Now()
	if startedDeployment(previous) && (previous.State == "needs_attention" || now.UnixMilli() >= previous.ExecutionDeadline) {
		return r.observeTerminal(ctx, previous)
	}
	if previous.State == "needs_attention" {
		return nil
	}
	if previous.ExecutionDeadline > 0 && now.UnixMilli() >= previous.ExecutionDeadline {
		_, err := store.New(r.database).ExpireOperation(ctx, store.ExpireOperationParams{ID: id, Now: now.UnixMilli()})
		return err
	}
	if previous.Attempts >= MaxAttempts {
		changed, err := store.New(r.database).ExhaustOperationRetries(ctx, store.ExhaustOperationRetriesParams{UpdatedAt: time.Now().UnixMilli(), ID: id})
		if err == nil && changed == 0 {
			return river.JobSnooze(progressPollInterval)
		}
		return err
	}
	operation, err := store.New(r.database).ClaimOperation(ctx, store.ClaimOperationParams{UpdatedAt: now.UnixMilli(), ExecutionDeadline: now.Add(MaxExecutionDuration).UnixMilli(), ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("claim operation: %w", err)
	}
	if now := time.Now().UnixMilli(); now >= operation.ExecutionDeadline {
		_, err := store.New(r.database).ExpireOperation(ctx, store.ExpireOperationParams{ID: id, Now: now})
		return err
	}
	result := Unknown
	if operation.State == "recovering" {
		result, err = r.executor.Reconcile(ctx, operation)
	} else {
		result, err = r.executor.Execute(ctx, operation)
	}
	bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err == nil && (result == Running || result == Recovering) {
		return r.observeProgress(bounded, operation, result)
	}
	state, outcome, reason := resultState(result, err, operation, riverAttempt)
	changed, persistErr := store.New(r.database).FinishOperation(bounded, store.FinishOperationParams{State: state, Outcome: outcome, Reason: reason, UpdatedAt: time.Now().UnixMilli(), ID: id, Fence: operation.Fence})
	if persistErr != nil {
		return errors.New("persist operation result failed")
	}
	if changed == 0 {
		return nil
	} // A newer reconciliation fence owns the observation.
	if state == "recovering" {
		return errors.New("operation outcome requires reconciliation")
	}
	return nil
}

func startedDeployment(operation store.Operation) bool {
	return operation.Started && (operation.Kind == "deploy" || operation.Kind == "rollback")
}

func (r *Runtime) observeTerminal(parent context.Context, operation store.Operation) error {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	result, err := r.executor.Reconcile(ctx, operation)
	if err == nil && (result == Succeeded || result == Recovered || result == Failed) {
		state, outcome, reason := resultState(result, nil, operation, 1)
		_, err := store.New(r.database).FinishOperation(ctx, store.FinishOperationParams{State: state, Outcome: outcome, Reason: reason, UpdatedAt: time.Now().UnixMilli(), ID: operation.ID, Fence: operation.Fence})
		return err
	}
	if _, err := store.New(r.database).ExpireOperation(ctx, store.ExpireOperationParams{ID: operation.ID, Now: time.Now().UnixMilli()}); err != nil {
		return err
	}
	// Snoozing refunds River's attempt; observation never renews effect authority.
	return river.JobSnooze(30 * time.Second)
}

func (r *Runtime) observeProgress(ctx context.Context, operation store.Operation, result Result) error {
	now := time.Now().UnixMilli()
	if now >= operation.ExecutionDeadline {
		_, err := store.New(r.database).ExpireOperation(ctx, store.ExpireOperationParams{ID: operation.ID, Now: now})
		return err
	}
	state, outcome, reason := "running", "unknown", "remote_running"
	if result == Recovering || operation.Outcome == "partial" {
		state, outcome, reason = "recovering", "partial", "recovery_running"
	}
	// Claim charged a potentially interrupted attempt. Only a verified native
	// progress result refunds that one charge; the fence rejects stale observers.
	changed, err := store.New(r.database).ObserveOperationProgress(ctx, store.ObserveOperationProgressParams{State: state, Outcome: outcome, Reason: reason, UpdatedAt: now, ID: operation.ID, Fence: operation.Fence})
	if err != nil {
		return errors.New("persist operation progress failed")
	}
	if changed == 0 {
		return nil
	}
	return river.JobSnooze(min(progressPollInterval, time.Duration(operation.ExecutionDeadline-now)*time.Millisecond))
}
func resultState(result Result, failure error, operation store.Operation, riverAttempt int) (state, outcome, reason string) {
	priorOutcome := "unknown"
	if result == Partial || operation.Outcome == "partial" {
		priorOutcome = "partial"
	}
	if failure == nil {
		switch result {
		case Succeeded:
			return "succeeded", "success", ""
		case Failed:
			return "failed", "failure", "confirmed_failure"
		case Recovered:
			return "recovered", "failure", "previous_release_restored"
		case Unsupported:
			if operation.State == "recovering" {
				return "needs_attention", priorOutcome, "executor_unavailable"
			}
			return "needs_attention", "pending", "executor_unavailable"
		}
	}
	if operation.Attempts >= MaxAttempts || riverAttempt >= MaxAttempts {
		return "needs_attention", priorOutcome, "reconciliation_exhausted"
	}
	if priorOutcome == "partial" {
		return "recovering", "partial", "partial_application"
	}
	return "recovering", "unknown", "remote_outcome_unknown"
}
