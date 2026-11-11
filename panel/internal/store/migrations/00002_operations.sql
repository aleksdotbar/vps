-- +goose Up
CREATE TABLE operations (
    id TEXT PRIMARY KEY NOT NULL,
    intent_json BLOB NOT NULL CHECK (length(intent_json) <= 4096),
    fingerprint BLOB NOT NULL CHECK (length(fingerprint) = 32),
    kind TEXT NOT NULL CHECK (kind IN ('enroll', 'deploy', 'rollback')),
    server_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    release_id TEXT NOT NULL,
    binding_version INTEGER NOT NULL,
    automatic BOOLEAN NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'recovering', 'recovered', 'superseded', 'needs_attention')),
    outcome TEXT NOT NULL CHECK (outcome IN ('pending', 'success', 'failure', 'partial', 'unknown')),
    reason TEXT NOT NULL DEFAULT '' CHECK (length(reason) <= 128),
    started BOOLEAN NOT NULL DEFAULT FALSE,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5),
    fence INTEGER NOT NULL DEFAULT 0 CHECK (fence >= 0),
    river_job_id INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX operations_one_unresolved_environment ON operations(environment_id)
WHERE environment_id != '' AND state IN ('queued','running','recovering','needs_attention');
CREATE INDEX operations_environment_unresolved ON operations(environment_id, state);
CREATE INDEX operations_server_unresolved ON operations(server_id, state);
CREATE INDEX operations_state ON operations(state);
-- +goose Down
DROP TABLE operations;
