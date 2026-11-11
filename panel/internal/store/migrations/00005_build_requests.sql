-- +goose Up
CREATE TABLE waiting_builds (
    id TEXT PRIMARY KEY NOT NULL,
    environment_id TEXT NOT NULL REFERENCES environments(id),
    server_id TEXT NOT NULL REFERENCES servers(id),
    intent_json BLOB NOT NULL CHECK(length(intent_json)<=4096),
    fingerprint BLOB NOT NULL CHECK(length(fingerprint)=32),
    selection_json BLOB NOT NULL CHECK(length(selection_json)<=65536),
    binding_version INTEGER NOT NULL,
    automatic BOOLEAN NOT NULL,
    state TEXT NOT NULL DEFAULT 'waiting' CHECK(state IN ('waiting','dispatch_unknown','queued','failed','superseded','needs_attention')),
    reason TEXT NOT NULL DEFAULT '',
    run_id INTEGER NOT NULL DEFAULT 0,
    run_attempt INTEGER NOT NULL DEFAULT 0,
    run_number INTEGER NOT NULL DEFAULT 0,
    artifact_id INTEGER NOT NULL DEFAULT 0,
    artifact_digest TEXT NOT NULL DEFAULT '',
    dispatch_started_at INTEGER NOT NULL DEFAULT 0,
    discovery_page INTEGER NOT NULL DEFAULT 1 CHECK(discovery_page BETWEEN 1 AND 10),
    poll_count INTEGER NOT NULL DEFAULT 0 CHECK(poll_count BETWEEN 0 AND 2880),
    failures INTEGER NOT NULL DEFAULT 0 CHECK(failures BETWEEN 0 AND 10),
    next_poll_at INTEGER NOT NULL,
    river_job_id INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX waiting_builds_one_environment ON waiting_builds(environment_id) WHERE state IN ('waiting','dispatch_unknown','needs_attention');
CREATE INDEX waiting_builds_state ON waiting_builds(state,next_poll_at);
CREATE INDEX waiting_builds_order ON waiting_builds(environment_id,binding_version,run_number DESC,run_attempt DESC);
CREATE INDEX waiting_builds_server ON waiting_builds(server_id,state);
CREATE TABLE github_branch_progress (
    environment_id TEXT PRIMARY KEY NOT NULL REFERENCES environments(id),
    binding_version INTEGER NOT NULL,
    discovery_page INTEGER NOT NULL DEFAULT 1 CHECK(discovery_page BETWEEN 1 AND 10),
    high_water_run INTEGER NOT NULL DEFAULT 0,
    high_water_attempt INTEGER NOT NULL DEFAULT 0,
    candidate_run_id INTEGER NOT NULL DEFAULT 0,
    candidate_attempt INTEGER NOT NULL DEFAULT 0,
    candidate_run_number INTEGER NOT NULL DEFAULT 0,
    candidate_sha TEXT NOT NULL DEFAULT '',
    manual_cutoff INTEGER NOT NULL DEFAULT 0,
    manual_attempt INTEGER NOT NULL DEFAULT 0,
    next_poll_at INTEGER NOT NULL DEFAULT 0
);
-- +goose Down
DROP TABLE github_branch_progress;
DROP TABLE waiting_builds;
