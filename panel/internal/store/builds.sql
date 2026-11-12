-- name: GetWaitingBuild :one
SELECT * FROM waiting_builds WHERE id=?;
-- name: WaitingBuildCapacity :one
SELECT count(*) AS retained,CAST(coalesce(sum(state IN ('waiting','dispatch_unknown','needs_attention')),0) AS INTEGER) AS unresolved FROM waiting_builds;
-- name: CreateWaitingBuild :exec
INSERT INTO waiting_builds(id,environment_id,server_id,intent_json,fingerprint,selection_json,binding_version,automatic,next_poll_at,created_at,updated_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?);
-- name: SetWaitingBuildJob :exec
UPDATE waiting_builds SET river_job_id=? WHERE id=?;
-- name: FinishWaitingBuild :exec
UPDATE waiting_builds SET state=?,reason=?,updated_at=? WHERE id=? AND state IN ('waiting','dispatch_unknown','needs_attention');
-- name: ScheduleWaitingBuild :exec
UPDATE waiting_builds SET next_poll_at=?,reason=?,poll_count=poll_count+1,failures=?,updated_at=? WHERE id=? AND state IN ('waiting','dispatch_unknown') AND poll_count<2880;
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
UPDATE github_branch_progress SET binding_version=?,handled_sha='',next_poll_at=0 WHERE environment_id=?;
-- name: SaveBranchProgress :exec
UPDATE github_branch_progress SET handled_sha=?,next_poll_at=? WHERE environment_id=?;
-- name: SetHandledBranchHead :exec
UPDATE github_branch_progress SET handled_sha=? WHERE environment_id=?;

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

