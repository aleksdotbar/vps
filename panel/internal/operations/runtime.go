package operations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riversqlite"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"

	"github.com/aleksdotbar/vps/panel/internal/store"
)

// Executor owns actual host effects. Execute must use operation ID and the
// target's durable exclusion/receipt protocol; Reconcile inspects that existing
// operation and must never initiate a second deployment because a lease expired.
// Reconcile must be read-only for expired or needs-attention work. Both methods honor cancellation.
type Executor interface {
	Execute(context.Context, store.Operation) (Result, error)
	Reconcile(context.Context, store.Operation) (Result, error)
}
type Result uint8

const (
	Unknown Result = iota
	Succeeded
	Failed
	Recovered
	Partial
	Unsupported
	// Running and Recovering require a fresh, identity-bound native observation.
	// They snooze within the retained deadline rather than consume failure retries.
	Running
	Recovering
)

type unsupportedExecutor struct{}

func (unsupportedExecutor) Execute(context.Context, store.Operation) (Result, error) {
	return Unsupported, nil
}
func (unsupportedExecutor) Reconcile(context.Context, store.Operation) (Result, error) {
	return Unsupported, nil
}

type Runtime struct {
	database *sql.DB
	client   *river.Client[*sql.Tx]
	executor Executor
	cancel   context.CancelFunc
	done     chan struct{}
}

// New runs River's own versioned migrations only after application migrations
// and master-key validation. No River SQL is copied into goose/sqlc migrations.
func New(ctx context.Context, database *sql.DB, executor Executor, assemble func(*river.Workers, *Runtime) ([]*river.PeriodicJob, error)) (*Runtime, error) {
	driver := riversqlite.New(database)
	migrator, err := rivermigrate.New(driver, &rivermigrate.Config{})
	if err != nil {
		return nil, fmt.Errorf("prepare River SQLite migrations: %w", err)
	}
	if _, err := migrator.ExistingVersions(ctx); err != nil {
		return nil, fmt.Errorf("check River schema compatibility: %w", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return nil, fmt.Errorf("migrate River SQLite schema: %w", err)
	}
	if executor == nil {
		executor = unsupportedExecutor{}
	}
	runtime := &Runtime{database: database, executor: executor}
	workers := river.NewWorkers()
	if err := river.AddWorkerSafely(workers, &operationWorker{runtime: runtime}); err != nil {
		return nil, fmt.Errorf("register operation worker: %w", err)
	}
	// Assembly only: no enqueue, network or Start until New returns.
	var periodicJobs []*river.PeriodicJob
	if assemble != nil {
		periodicJobs, err = assemble(workers, runtime)
		if err != nil {
			return nil, err
		}
	}
	client, err := river.NewClient(driver, &river.Config{
		Queues: map[string]river.QueueConfig{queueName: {MaxWorkers: 2}, "github": {MaxWorkers: 2}, "readiness": {MaxWorkers: 1}}, Workers: workers,
		PeriodicJobs: periodicJobs, MaxAttempts: MaxAttempts, JobTimeout: 2 * time.Minute, RescueStuckJobsAfter: 3 * time.Minute,
		SoftStopTimeout: 10 * time.Second, FetchCooldown: 100 * time.Millisecond, FetchPollInterval: time.Second,
		CompletedJobRetentionPeriod: 24 * time.Hour, CancelledJobRetentionPeriod: 24 * time.Hour, DiscardedJobRetentionPeriod: 24 * time.Hour,
		Logger: slog.Default(),
	})
	if err != nil {
		return nil, fmt.Errorf("configure River SQLite: %w", err)
	}
	runtime.client = client
	return runtime, nil
}
func (r *Runtime) Start(ctx context.Context) error {
	if r.cancel != nil {
		return errors.New("operation runtime already started")
	}
	if err := store.New(r.database).ReconcileStartedOperations(ctx, time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("mark interrupted operations for reconciliation: %w", err)
	}
	if err := r.ReconcileState(ctx); err != nil {
		return err
	}
	if err := r.client.Start(ctx); err != nil {
		return fmt.Errorf("start operation workers: %w", err)
	}
	monitorCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.done = make(chan struct{})
	go func() {
		defer close(r.done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-monitorCtx.Done():
				return
			case <-ticker.C:
				bounded, cancel := context.WithTimeout(monitorCtx, 5*time.Second)
				err := r.ReconcileState(bounded)
				cancel()
				if err != nil && !errors.Is(err, context.Canceled) {
					slog.Error("operation queue reconciliation failed")
				}
			}
		}
	}()
	return nil
}
func (r *Runtime) Stop(ctx context.Context) error {
	if r.cancel == nil {
		return nil
	}
	r.cancel()
	select {
	case <-r.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := r.client.Stop(ctx); err != nil {
		return fmt.Errorf("stop operation workers: %w", err)
	}
	r.cancel = nil
	return nil
}

// ReconcileState bounds a sweep to 128 unresolved records and one River batch
// read. A discarded/missing job cannot silently strand a domain operation.
func (r *Runtime) ReconcileState(ctx context.Context) error {
	operations, err := store.New(r.database).UnresolvedOperations(ctx)
	if err != nil {
		return fmt.Errorf("read unresolved operations: %w", err)
	}
	ids := make([]int64, 0, len(operations))
	for _, operation := range operations {
		if operation.State != "needs_attention" || startedDeployment(operation) {
			ids = append(ids, operation.RiverJobID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	jobs, err := r.client.JobList(ctx, river.NewJobListParams().IDs(ids...).States().First(MaxUnresolved))
	if err != nil {
		return fmt.Errorf("read pending River jobs: %w", err)
	}
	states := make(map[int64]rivertype.JobState, len(jobs.Jobs))
	for _, job := range jobs.Jobs {
		states[job.ID] = job.State
	}
	for _, operation := range operations {
		if operation.State == "needs_attention" && !startedDeployment(operation) {
			continue
		}
		now := time.Now().UnixMilli()
		if operation.ExecutionDeadline > 0 && now >= operation.ExecutionDeadline {
			if _, err := store.New(r.database).ExpireOperation(ctx, store.ExpireOperationParams{ID: operation.ID, Now: now}); err != nil {
				return fmt.Errorf("record operation deadline: %w", err)
			}
			if !startedDeployment(operation) {
				continue
			}
			operation.State = "needs_attention"
		}
		state, found := states[operation.RiverJobID]
		exhausted := !found || state == rivertype.JobStateDiscarded || state == rivertype.JobStateCancelled || state == rivertype.JobStateCompleted
		if startedDeployment(operation) && operation.State == "needs_attention" && found {
			if exhausted {
				if _, err := r.client.JobRetry(ctx, operation.RiverJobID); err != nil {
					return fmt.Errorf("schedule terminal observation: %w", err)
				}
			}
			continue
		}
		if exhausted {
			if err := store.New(r.database).ExhaustOperation(ctx, store.ExhaustOperationParams{UpdatedAt: time.Now().UnixMilli(), ID: operation.ID}); err != nil {
				return fmt.Errorf("record exhausted operation: %w", err)
			}
		} else if operation.Attempts >= MaxAttempts && state != rivertype.JobStateRunning {
			// A progress receipt can refund the claim after our snapshot. Recheck
			// the failure budget in the write, rather than exhaust fresh progress.
			if _, err := store.New(r.database).ExhaustOperationRetries(ctx, store.ExhaustOperationRetriesParams{UpdatedAt: time.Now().UnixMilli(), ID: operation.ID}); err != nil {
				return fmt.Errorf("record exhausted retries: %w", err)
			}
		}
	}
	return nil
}
