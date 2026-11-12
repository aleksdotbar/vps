-- +goose Up
-- Releases resolve each service's GIT_SHA image tag from GHCR. The panel no
-- longer dispatches workflows, follows runs or downloads release artifacts.
DROP INDEX waiting_builds_order;
ALTER TABLE waiting_builds DROP COLUMN run_id;
ALTER TABLE waiting_builds DROP COLUMN run_attempt;
ALTER TABLE waiting_builds DROP COLUMN run_number;
ALTER TABLE waiting_builds DROP COLUMN artifact_id;
ALTER TABLE waiting_builds DROP COLUMN artifact_digest;
ALTER TABLE waiting_builds DROP COLUMN dispatch_started_at;
ALTER TABLE waiting_builds DROP COLUMN discovery_page;
-- Automatic deploys follow branch heads: handled_sha is the last head already
-- admitted, or blocked by an explicit deploy or rollback.
DROP TABLE github_branch_progress;
CREATE TABLE github_branch_progress (
    environment_id TEXT PRIMARY KEY NOT NULL REFERENCES environments(id),
    binding_version INTEGER NOT NULL,
    handled_sha TEXT NOT NULL DEFAULT '' CHECK (handled_sha='' OR length(handled_sha)=40),
    next_poll_at INTEGER NOT NULL DEFAULT 0
);
ALTER TABLE projects DROP COLUMN workflow_id;
ALTER TABLE releases DROP COLUMN workflow_id;
ALTER TABLE releases DROP COLUMN run_id;
ALTER TABLE releases DROP COLUMN run_attempt;

-- +goose Down
ALTER TABLE releases ADD COLUMN run_attempt INTEGER NOT NULL DEFAULT 0;
ALTER TABLE releases ADD COLUMN run_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE releases ADD COLUMN workflow_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projects ADD COLUMN workflow_id INTEGER NOT NULL DEFAULT 0;
DROP TABLE github_branch_progress;
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
ALTER TABLE waiting_builds ADD COLUMN discovery_page INTEGER NOT NULL DEFAULT 1 CHECK(discovery_page BETWEEN 1 AND 10);
ALTER TABLE waiting_builds ADD COLUMN dispatch_started_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE waiting_builds ADD COLUMN artifact_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE waiting_builds ADD COLUMN artifact_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE waiting_builds ADD COLUMN run_number INTEGER NOT NULL DEFAULT 0;
ALTER TABLE waiting_builds ADD COLUMN run_attempt INTEGER NOT NULL DEFAULT 0;
ALTER TABLE waiting_builds ADD COLUMN run_id INTEGER NOT NULL DEFAULT 0;
CREATE INDEX waiting_builds_order ON waiting_builds(environment_id,binding_version,run_number DESC,run_attempt DESC);
