-- name: GetWaitingBuild :one
SELECT * FROM waiting_builds WHERE id=?;
-- name: WaitingBuildCapacity :one
SELECT count(*) AS retained,CAST(coalesce(sum(state IN ('waiting','dispatch_unknown','needs_attention')),0) AS INTEGER) AS unresolved FROM waiting_builds;
-- name: CreateWaitingBuild :exec
INSERT INTO waiting_builds(id,environment_id,server_id,intent_json,fingerprint,selection_json,binding_version,automatic,next_poll_at,created_at,updated_at,expires_at,run_id,run_attempt,run_number) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?);
-- name: SetWaitingBuildJob :exec
UPDATE waiting_builds SET river_job_id=? WHERE id=?;
-- name: SetWaitingBuildRun :exec
UPDATE waiting_builds SET state='waiting',run_id=?,run_attempt=?,run_number=?,discovery_page=1,failures=0,updated_at=? WHERE id=? AND state IN ('waiting','dispatch_unknown');
-- name: StartWaitingBuildDispatch :execrows
UPDATE waiting_builds SET state='dispatch_unknown',dispatch_started_at=?,discovery_page=1,updated_at=? WHERE id=? AND state='waiting' AND dispatch_started_at=0;
-- name: FinishWaitingBuild :exec
UPDATE waiting_builds SET state=?,reason=?,updated_at=? WHERE id=? AND state IN ('waiting','dispatch_unknown','needs_attention');
-- name: ScheduleWaitingBuild :exec
UPDATE waiting_builds SET next_poll_at=?,discovery_page=?,poll_count=poll_count+1,failures=?,updated_at=? WHERE id=? AND state IN ('waiting','dispatch_unknown') AND poll_count<2880;
-- name: SupersedeWaitingAutomatic :exec
UPDATE waiting_builds SET state='superseded',reason='manual_intent',updated_at=? WHERE environment_id=? AND automatic AND state IN ('waiting','dispatch_unknown');
-- name: SupersedeDisabledWaitingBuilds :exec
UPDATE waiting_builds SET state='superseded',reason='automatic_deploy_disabled',updated_at=? WHERE environment_id=? AND automatic AND state IN ('waiting','dispatch_unknown');
-- name: SupersedeStaleWaitingBuilds :exec
UPDATE waiting_builds SET state='superseded',reason='binding_changed',updated_at=? WHERE environment_id=? AND binding_version!=? AND state IN ('waiting','dispatch_unknown');
-- name: ActiveWaitingBuildForEnvironment :one
SELECT * FROM waiting_builds WHERE environment_id=? AND state IN ('waiting','dispatch_unknown','needs_attention') LIMIT 1;
-- name: PollableWaitingBuilds :many
SELECT * FROM waiting_builds WHERE state IN ('waiting','dispatch_unknown') ORDER BY next_poll_at,id LIMIT 128;
-- name: AutoDeployEnvironments :many
SELECT * FROM environments WHERE auto_deploy ORDER BY id LIMIT 512;
-- name: GetBranchProgress :one
SELECT * FROM github_branch_progress WHERE environment_id=?;
-- name: InitializeBranchProgress :exec
INSERT INTO github_branch_progress(environment_id,binding_version) VALUES(?,?) ON CONFLICT(environment_id) DO NOTHING;
-- name: ResetBranchProgress :exec
UPDATE github_branch_progress SET binding_version=?,discovery_page=1,high_water_run=0,high_water_attempt=0,candidate_run_id=0,candidate_attempt=0,candidate_run_number=0,candidate_sha='',manual_cutoff=0,manual_attempt=0,next_poll_at=0 WHERE environment_id=?;
-- name: SaveBranchProgress :exec
UPDATE github_branch_progress SET discovery_page=?,high_water_run=?,high_water_attempt=?,candidate_run_id=?,candidate_attempt=?,candidate_run_number=?,candidate_sha=?,next_poll_at=? WHERE environment_id=?;
-- name: SetManualBuildCutoff :exec
UPDATE github_branch_progress SET manual_cutoff=?,manual_attempt=?,candidate_run_id=0,candidate_attempt=0,candidate_run_number=0,candidate_sha='' WHERE environment_id=?;

-- name: SupersedeQueuedAutomaticForManual :exec
UPDATE operations SET state='superseded',reason='superseded_by_manual',updated_at=? WHERE environment_id=? AND automatic AND state='queued' AND NOT started;
-- name: HostSetupPending :one
SELECT EXISTS(SELECT 1 FROM operations WHERE server_id=? AND kind='enroll' AND state IN ('queued','running','recovering','needs_attention'));
-- name: WaitingServerCount :one
SELECT count(*) FROM waiting_builds WHERE server_id=? AND state IN ('waiting','dispatch_unknown','needs_attention');
-- name: SupersedeWaitingForManual :exec
UPDATE waiting_builds SET state='superseded',reason='manual_intent',updated_at=? WHERE environment_id=? AND state IN ('waiting','dispatch_unknown','needs_attention');
-- name: SupersedeQueuedAutomaticCandidate :exec
UPDATE operations SET state='superseded',reason='newer_build',updated_at=? WHERE id=? AND automatic AND state='queued' AND NOT started;
-- name: DeferBranchPoll :exec
UPDATE github_branch_progress SET next_poll_at=? WHERE environment_id=? AND binding_version=?;
-- name: HighestWaitingRun :one
SELECT run_number,run_attempt FROM waiting_builds WHERE environment_id=? AND binding_version=? ORDER BY run_number DESC,run_attempt DESC LIMIT 1;

-- name: BindWaitingArtifact :execrows
UPDATE waiting_builds SET artifact_id=sqlc.arg(artifact_id),artifact_digest=sqlc.arg(artifact_digest) WHERE id=sqlc.arg(id) AND run_id=sqlc.arg(run_id) AND run_attempt=sqlc.arg(run_attempt) AND state='waiting' AND artifact_id IN (0,sqlc.arg(artifact_id)) AND (artifact_digest='' OR artifact_digest=sqlc.arg(artifact_digest));
