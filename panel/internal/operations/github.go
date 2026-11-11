package operations

import (
	"context"
	"database/sql"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// GitHub job payloads carry stable database identities only.
type BuildArgs struct {
	RequestID string `json:"request_id"`
}

func (BuildArgs) Kind() string { return "github_build" }

type DiscoveryArgs struct {
	EnvironmentID string `json:"environment_id"`
}

func (DiscoveryArgs) Kind() string { return "github_discovery" }
func (r *Runtime) EnqueueBuild(ctx context.Context, tx *sql.Tx, id string) (int64, error) {
	result, err := r.client.InsertTx(ctx, tx, BuildArgs{RequestID: id}, &river.InsertOpts{Queue: "github", MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true}})
	if err != nil {
		return 0, err
	}
	return result.Job.ID, nil
}
func (r *Runtime) EnqueueDiscovery(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := r.client.InsertTx(ctx, tx, DiscoveryArgs{EnvironmentID: id}, &river.InsertOpts{Queue: "github", MaxAttempts: 5, UniqueOpts: DiscoveryUnique()})
	return err
}

// IntentIdentity canonicalizes the existing nonsecret operation contract. It
// must never receive credential values or secret-write input.
func IntentIdentity(intent Intent) (Intent, []byte, error) {
	canonical, err := normalize(intent)
	if err != nil {
		return Intent{}, nil, err
	}
	identity, err := fingerprint(canonical)
	return canonical, identity, err
}

// Discovery uniqueness excludes completed jobs but includes every pending state,
// bounding outstanding work to one job per environment across periodic sweeps.
func DiscoveryUnique() river.UniqueOpts {
	return river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable, rivertype.JobStateRunning, rivertype.JobStateScheduled}}
}
func (r *Runtime) BuildJobsTerminal(ctx context.Context, ids []int64) (map[int64]bool, error) {
	result := make(map[int64]bool, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	if len(ids) > 128 {
		return nil, ErrCapacity
	}
	jobs, err := r.client.JobList(ctx, river.NewJobListParams().IDs(ids...).States().First(128))
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		result[id] = true
	}
	for _, job := range jobs.Jobs {
		result[job.ID] = job.State == rivertype.JobStateCompleted || job.State == rivertype.JobStateCancelled || job.State == rivertype.JobStateDiscarded
	}
	return result, nil
}
