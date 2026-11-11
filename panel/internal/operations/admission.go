package operations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/aleksdotbar/vps/panel/internal/store"
)

const (
	MaxUnresolved = 128
	MaxRetained   = 100000
	MaxAttempts   = 5
	queueName     = "operations"
)

var (
	ErrInvalidIntent = errors.New("invalid operation intent")
	ErrRequestReused = errors.New("request identity reused for different intent")
	ErrConflict      = errors.New("unresolved operation conflicts with request")
	ErrCapacity      = errors.New("operation capacity reached")
)

type Kind string

const (
	Enroll   Kind = "enroll"
	Deploy   Kind = "deploy"
	Rollback Kind = "rollback"
)

// Intent contains only stable caller input. Never add secret values or use this
// unkeyed fingerprint scheme for secret-write idempotency. Derived release/server
// IDs belong to Assignment so retries can replay before creating snapshots.
type Intent struct {
	RequestID       string           `json:"request_id"`
	Kind            Kind             `json:"kind"`
	EnvironmentID   string           `json:"environment_id,omitempty"`
	ExpectedVersion int64            `json:"expected_version,omitempty"`
	SourceSHA       string           `json:"source_sha,omitempty"`
	TargetReleaseID string           `json:"target_release_id,omitempty"`
	Automatic       bool             `json:"automatic"`
	Enrollment      *EnrollmentInput `json:"enrollment,omitempty"`
}
type EnrollmentInput struct {
	Name                  string `json:"name"`
	Address               string `json:"address"`
	Fingerprint           string `json:"fingerprint"`
	User                  string `json:"user"`
	BootstrapCredentialID string `json:"bootstrap_credential_id"`
}
type Assignment struct {
	ServerID       string
	EnvironmentID  string
	ReleaseID      string
	BindingVersion int64
}

func normalize(intent Intent) (Intent, error) {
	requestID, err := uuid.Parse(intent.RequestID)
	if err != nil {
		return Intent{}, ErrInvalidIntent
	}
	intent.RequestID = requestID.String()
	if intent.Automatic && intent.Kind != Deploy {
		return Intent{}, ErrInvalidIntent
	}
	switch intent.Kind {
	case Enroll:
		if intent.Enrollment == nil || intent.EnvironmentID != "" || intent.SourceSHA != "" || intent.TargetReleaseID != "" || intent.ExpectedVersion != 0 {
			return Intent{}, ErrInvalidIntent
		}
		enrollment := *intent.Enrollment
		if enrollment.Name == "" || len(enrollment.Name) > 100 || enrollment.Address == "" || len(enrollment.Address) > 253 || enrollment.Fingerprint == "" || len(enrollment.Fingerprint) > 128 || enrollment.User == "" || len(enrollment.User) > 32 {
			return Intent{}, ErrInvalidIntent
		}
		credentialID, err := uuid.Parse(enrollment.BootstrapCredentialID)
		if err != nil {
			return Intent{}, ErrInvalidIntent
		}
		enrollment.BootstrapCredentialID = credentialID.String()
		intent.Enrollment = &enrollment
	case Deploy, Rollback:
		environmentID, err := uuid.Parse(intent.EnvironmentID)
		if err != nil || intent.Enrollment != nil || intent.ExpectedVersion < 1 {
			return Intent{}, ErrInvalidIntent
		}
		intent.EnvironmentID = environmentID.String()
		if intent.Kind == Deploy {
			source, err := hex.DecodeString(intent.SourceSHA)
			if err != nil || len(source) != 20 || intent.TargetReleaseID != "" {
				return Intent{}, ErrInvalidIntent
			}
			intent.SourceSHA = hex.EncodeToString(source)
		} else {
			releaseID, err := uuid.Parse(intent.TargetReleaseID)
			if err != nil || intent.SourceSHA != "" {
				return Intent{}, ErrInvalidIntent
			}
			intent.TargetReleaseID = releaseID.String()
		}
	default:
		return Intent{}, ErrInvalidIntent
	}
	return intent, nil
}
func fingerprint(intent Intent) ([]byte, error) {
	encoded, err := json.Marshal(intent)
	if err != nil {
		return nil, ErrInvalidIntent
	}
	sum := sha256.Sum256(encoded)
	return sum[:], nil
}

// Replay must be called inside the caller's immediate transaction before P04/P07
// allocate derived IDs or create immutable snapshots. The same transaction then
// calls Enqueue and commits all state together; no external effects occur here.
func (r *Runtime) Replay(ctx context.Context, tx *sql.Tx, intent Intent) (store.Operation, bool, error) {
	intent, err := normalize(intent)
	if err != nil {
		return store.Operation{}, false, err
	}
	expected, err := fingerprint(intent)
	if err != nil {
		return store.Operation{}, false, err
	}
	if _, err := store.New(tx).GetMetadataMutation(ctx, intent.RequestID); err == nil {
		return store.Operation{}, false, ErrRequestReused
	} else if !errors.Is(err, sql.ErrNoRows) {
		return store.Operation{}, false, fmt.Errorf("check metadata identity: %w", err)
	}
	existing, err := store.New(tx).GetOperation(ctx, intent.RequestID)
	if errors.Is(err, sql.ErrNoRows) {
		waiting, readErr := store.New(tx).GetWaitingBuild(ctx, intent.RequestID)
		if errors.Is(readErr, sql.ErrNoRows) {
			return store.Operation{}, false, nil
		}
		if readErr != nil {
			return store.Operation{}, false, readErr
		}
		if !bytes.Equal(expected, waiting.Fingerprint) {
			return store.Operation{}, false, ErrRequestReused
		}
		if waiting.State != "waiting" && waiting.State != "dispatch_unknown" {
			return store.Operation{}, false, ErrConflict
		}
		return store.Operation{}, false, nil
	}
	if err != nil {
		return store.Operation{}, false, fmt.Errorf("look up operation identity: %w", err)
	}
	if !bytes.Equal(expected, existing.Fingerprint) {
		return store.Operation{}, false, ErrRequestReused
	}
	return existing, true, nil
}
func (r *Runtime) Enqueue(ctx context.Context, tx *sql.Tx, intent Intent, target Assignment) (store.Operation, error) {
	intent, err := normalize(intent)
	if err != nil {
		return store.Operation{}, err
	}
	if existing, replay, err := r.Replay(ctx, tx, intent); err != nil || replay {
		return existing, err
	}
	serverID, err := uuid.Parse(target.ServerID)
	if err != nil {
		return store.Operation{}, ErrInvalidIntent
	}
	target.ServerID = serverID.String()
	if intent.Kind == Enroll {
		if target.EnvironmentID != "" || target.ReleaseID != "" || target.BindingVersion != 0 {
			return store.Operation{}, ErrInvalidIntent
		}
	} else {
		environmentID, err := uuid.Parse(target.EnvironmentID)
		if err != nil || environmentID.String() != intent.EnvironmentID || target.BindingVersion < 1 {
			return store.Operation{}, ErrInvalidIntent
		}
		target.EnvironmentID = environmentID.String()
		releaseID, err := uuid.Parse(target.ReleaseID)
		if err != nil {
			return store.Operation{}, ErrInvalidIntent
		}
		target.ReleaseID = releaseID.String()
		if intent.Kind == Rollback && target.ReleaseID != intent.TargetReleaseID {
			return store.Operation{}, ErrInvalidIntent
		}
	}
	queries := store.New(tx)
	if intent.Kind == Enroll {
		count, err := queries.WaitingServerCount(ctx, target.ServerID)
		if err != nil {
			return store.Operation{}, err
		}
		if count > 0 {
			return store.Operation{}, ErrConflict
		}
	}

	if intent.Kind == Rollback {
		if err := advanceRollbackCutoff(ctx, queries, intent.EnvironmentID); err != nil {
			return store.Operation{}, err
		}
		if err := store.New(tx).SupersedeWaitingAutomatic(ctx, store.SupersedeWaitingAutomaticParams{UpdatedAt: time.Now().UnixMilli(), EnvironmentID: intent.EnvironmentID}); err != nil {
			return store.Operation{}, err
		}
	}
	conflicts, err := queries.UnresolvedServerOperations(ctx, target.ServerID)
	if err != nil {
		return store.Operation{}, fmt.Errorf("check operation conflicts: %w", err)
	}
	for _, existing := range conflicts {
		conflict := existing.Kind == string(Enroll) || intent.Kind == Enroll || existing.EnvironmentID == target.EnvironmentID
		if !conflict {
			continue
		}
		if intent.Kind == Rollback && existing.EnvironmentID == target.EnvironmentID && existing.Automatic && !existing.Started && existing.State == "queued" {
			if err := queries.SupersedeQueuedOperation(ctx, store.SupersedeQueuedOperationParams{UpdatedAt: time.Now().UnixMilli(), ID: existing.ID}); err != nil {
				return store.Operation{}, fmt.Errorf("supersede queued automatic intent: %w", err)
			}
			continue
		}
		return store.Operation{}, ErrConflict
	}
	capacity, err := queries.OperationCapacity(ctx)
	if err != nil {
		return store.Operation{}, fmt.Errorf("check operation capacity: %w", err)
	}
	if capacity.Total >= MaxRetained || capacity.Unresolved >= MaxUnresolved {
		return store.Operation{}, ErrCapacity
	}
	hash, err := fingerprint(intent)
	if err != nil {
		return store.Operation{}, err
	}
	encoded, err := json.Marshal(intent)
	if err != nil || len(encoded) > 4096 {
		return store.Operation{}, ErrInvalidIntent
	}
	now := time.Now().UnixMilli()
	if err := queries.CreateOperation(ctx, store.CreateOperationParams{ID: intent.RequestID, IntentJson: encoded, Fingerprint: hash, Kind: string(intent.Kind), ServerID: target.ServerID, EnvironmentID: target.EnvironmentID, ReleaseID: target.ReleaseID, BindingVersion: target.BindingVersion, Automatic: intent.Automatic, CreatedAt: now, UpdatedAt: now}); err != nil {
		return store.Operation{}, fmt.Errorf("persist operation intent: %w", err)
	}
	job, err := r.client.InsertTx(ctx, tx, jobArgs{OperationID: intent.RequestID}, &river.InsertOpts{Queue: queueName, MaxAttempts: MaxAttempts})
	if err != nil {
		return store.Operation{}, fmt.Errorf("enqueue operation atomically: %w", err)
	}
	if err := queries.SetOperationJob(ctx, store.SetOperationJobParams{RiverJobID: job.Job.ID, ID: intent.RequestID}); err != nil {
		return store.Operation{}, fmt.Errorf("record operation job identity: %w", err)
	}
	if intent.Kind == Deploy {
		if err := queries.FinishWaitingBuild(ctx, store.FinishWaitingBuildParams{State: "queued", Reason: "release_verified", UpdatedAt: now, ID: intent.RequestID}); err != nil {
			return store.Operation{}, err
		}
	}
	return queries.GetOperation(ctx, intent.RequestID)
}

// Explicit rollback blocks every already persisted automatic candidate. The P10
// provider boundary additionally captures the latest provider run order outside
// this transaction before admission, covering as-yet undiscovered older runs.
func advanceRollbackCutoff(ctx context.Context, queries *store.Queries, environmentID string) error {
	progress, err := queries.GetBranchProgress(ctx, environmentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	number, attempt := progress.ManualCutoff, progress.ManualAttempt
	advance := func(n, a int64) {
		if n > number || (n == number && a > attempt) {
			number = n
			attempt = a
		}
	}
	advance(progress.HighWaterRun, progress.HighWaterAttempt)
	advance(progress.CandidateRunNumber, progress.CandidateAttempt)
	waiting, err := queries.HighestWaitingRun(ctx, store.HighestWaitingRunParams{EnvironmentID: environmentID, BindingVersion: progress.BindingVersion})
	if err == nil {
		advance(waiting.RunNumber, waiting.RunAttempt)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return queries.SetManualBuildCutoff(ctx, store.SetManualBuildCutoffParams{EnvironmentID: environmentID, ManualCutoff: number, ManualAttempt: attempt})
}
