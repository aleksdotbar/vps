-- name: GetDeploymentExecution :one
SELECT * FROM deployment_execution WHERE operation_id=?;

-- name: CaptureDeploymentExecution :exec
INSERT INTO deployment_execution(operation_id,previous_release_id,registry_username,registry_version,created_at)
VALUES(?,?,?,?,?);

-- name: PromoteEnvironmentRelease :exec
UPDATE environments SET current_release_id=?,previous_release_id=?,applied_version=? WHERE id=?;

-- name: ClearServiceObservations :exec
DELETE FROM service_observations WHERE environment_id=?;

-- name: RecordServiceObservation :exec
INSERT INTO service_observations(environment_id,name,release_id,operation_id,image_digest,container_id,state,health,observed_at)
VALUES(?,?,?,?,?,?,'running','healthy',?);

-- name: ListObservedReleaseServices :many
SELECT r.name,r.image_digest,o.release_id AS observed_release_id,o.image_digest AS observed_image_digest,o.state,o.health,o.observed_at,
    CAST(EXISTS(SELECT 1 FROM operations active WHERE active.environment_id=sqlc.arg(environment_id) AND active.started AND active.state IN ('running','recovering','needs_attention')) AS INTEGER) AS execution_pending
FROM release_services r
LEFT JOIN service_observations o ON o.environment_id=sqlc.arg(environment_id) AND o.name=r.name AND o.release_id=r.release_id
WHERE r.release_id=sqlc.arg(release_id) AND r.name>sqlc.arg(after_name)
ORDER BY r.name LIMIT sqlc.arg(page_limit);

-- name: RecordOperationDiagnostics :exec
INSERT INTO operation_diagnostics(operation_id,diagnostics_json,recorded_at) VALUES(?,?,?)
ON CONFLICT(operation_id) DO UPDATE SET diagnostics_json=excluded.diagnostics_json, recorded_at=excluded.recorded_at
WHERE operation_diagnostics.diagnostics_json!=excluded.diagnostics_json;

-- name: GetOperationDiagnostics :one
SELECT diagnostics_json FROM operation_diagnostics WHERE operation_id=?;
