-- name: GetAdministrator :one
SELECT * FROM administrator WHERE id = 1;

-- name: CreateAdministrator :exec
INSERT INTO administrator(id, password_hash, generation, key_check) VALUES(1, ?, 1, ?);

-- name: ResetAdministrator :exec
UPDATE administrator SET password_hash = ?, generation = generation + 1 WHERE id = 1;

-- name: DeleteSessions :exec
DELETE FROM sessions;

-- WORKAROUND: sqlc does not rewrite named macros inside SQLite UPSERT clauses.
-- name: ClaimLoginAttempt :one
INSERT INTO login_budget(id, window_start, attempts) VALUES(1, ?1, 1)
ON CONFLICT(id) DO UPDATE SET
    window_start = CASE WHEN window_start <= ?1 - 900 THEN ?1 ELSE window_start END,
    attempts = CASE WHEN window_start <= ?1 - 900 THEN 1 ELSE attempts + 1 END
WHERE window_start <= ?1 - 900 OR attempts < 10
RETURNING attempts;

-- name: ClearLoginBudget :exec
DELETE FROM login_budget;

-- name: GetEncryptedVersion :one
SELECT * FROM encrypted_versions WHERE kind = ? AND owner_id = ? AND version = ?;

-- name: GetLatestEncryptedVersion :one
SELECT * FROM encrypted_versions WHERE kind = ? AND owner_id = ? ORDER BY version DESC LIMIT 1;

-- name: CreateEncryptedVersion :exec
INSERT INTO encrypted_versions(kind, owner_id, version, ciphertext) VALUES (?, ?, ?, ?);

-- name: GetOperation :one
SELECT * FROM operations WHERE id = ?;

-- name: CreateOperation :exec
INSERT INTO operations(id, intent_json, fingerprint, kind, server_id, environment_id, release_id, binding_version, automatic, state, outcome, created_at, updated_at, build_request_id)
VALUES (sqlc.arg(id), sqlc.arg(intent_json), sqlc.arg(fingerprint), sqlc.arg(kind), sqlc.arg(server_id), sqlc.arg(environment_id), sqlc.arg(release_id), sqlc.arg(binding_version), sqlc.arg(automatic), 'queued', 'pending', sqlc.arg(created_at), sqlc.arg(updated_at), (SELECT id FROM waiting_builds WHERE id=sqlc.arg(id)));

-- name: SetOperationJob :exec
UPDATE operations SET river_job_id = ? WHERE id = ?;

-- name: OperationCapacity :one
SELECT (SELECT count(*) FROM operations) AS total,
       (SELECT count(*) FROM operations WHERE state IN ('queued','running','recovering','needs_attention')) AS unresolved;

-- name: UnresolvedServerOperations :many
SELECT * FROM operations WHERE server_id = ? AND state IN ('queued','running','recovering','needs_attention') LIMIT 128;

-- name: UnresolvedOperations :many
SELECT * FROM operations WHERE state IN ('queued','running','recovering','needs_attention') ORDER BY created_at, id LIMIT 128;

-- name: SupersedeQueuedOperation :exec
UPDATE operations SET state='superseded', outcome='pending', reason='superseded_by_rollback', updated_at=?
WHERE id=? AND state='queued' AND NOT started AND automatic;

-- name: ClaimOperation :one
UPDATE operations SET started=TRUE, attempts=attempts+1, fence=fence+1,
    execution_deadline=CASE WHEN execution_deadline=0 THEN sqlc.arg(execution_deadline) ELSE execution_deadline END,
    state=CASE WHEN started THEN 'recovering' ELSE 'running' END,
    outcome=CASE WHEN outcome='partial' THEN 'partial' ELSE 'unknown' END, updated_at=sqlc.arg(updated_at)
WHERE id=sqlc.arg(id) AND state IN ('queued','running','recovering') AND attempts < 5
RETURNING *;

-- name: FinishOperation :execrows
UPDATE operations SET state=?, outcome=?, reason=?, updated_at=?
WHERE id=? AND fence=? AND state IN ('running','recovering','needs_attention');

-- name: ObserveOperationProgress :execrows
UPDATE operations SET state=?, outcome=?, reason=?, attempts=attempts-1, updated_at=?
WHERE id=? AND fence=? AND started AND attempts>0 AND state IN ('running','recovering');

-- name: ExpireOperation :execrows
UPDATE operations SET state='needs_attention',
    outcome=CASE WHEN outcome='partial' THEN 'partial' WHEN started THEN 'unknown' ELSE 'pending' END,
    reason='execution_deadline_exceeded', updated_at=sqlc.arg(now)
WHERE id=sqlc.arg(id) AND state IN ('queued','running','recovering')
    AND execution_deadline>0 AND execution_deadline<=sqlc.arg(now);

-- name: ReconcileStartedOperations :exec
UPDATE operations SET state='recovering', outcome=CASE WHEN outcome='partial' THEN 'partial' ELSE 'unknown' END, updated_at=?
WHERE started AND state IN ('queued','running','recovering');

-- name: ExhaustOperation :exec
UPDATE operations SET state='needs_attention', outcome=CASE WHEN outcome='partial' THEN 'partial' WHEN started THEN 'unknown' ELSE 'pending' END,
    reason='reconciliation_exhausted', updated_at=?
WHERE id=? AND state IN ('queued','running','recovering');

-- name: ExhaustOperationRetries :execrows
UPDATE operations SET state='needs_attention', outcome=CASE WHEN outcome='partial' THEN 'partial' WHEN started THEN 'unknown' ELSE 'pending' END,
    reason='reconciliation_exhausted', updated_at=?
WHERE id=? AND attempts>=5 AND state IN ('queued','running','recovering');
