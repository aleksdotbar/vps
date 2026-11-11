-- +goose Up
CREATE TABLE deployment_execution (
    operation_id TEXT PRIMARY KEY NOT NULL REFERENCES operations(id),
    previous_release_id TEXT REFERENCES releases(id),
    registry_username TEXT NOT NULL DEFAULT '',
    registry_version INTEGER,
    credential_kind TEXT NOT NULL DEFAULT 'credential' CHECK(credential_kind='credential'),
    credential_owner TEXT NOT NULL DEFAULT 'ghcr-read' CHECK(credential_owner='ghcr-read'),
    created_at INTEGER NOT NULL CHECK(created_at>0),
    FOREIGN KEY(credential_kind,credential_owner,registry_version) REFERENCES encrypted_versions(kind,owner_id,version),
    CHECK((registry_version IS NULL AND registry_username='') OR (registry_version>0 AND length(registry_username)>0))
);
-- +goose StatementBegin
CREATE TRIGGER deployment_execution_immutable BEFORE UPDATE ON deployment_execution
BEGIN SELECT RAISE(ABORT,'deployment execution input is immutable'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER deployment_execution_retained BEFORE DELETE ON deployment_execution
BEGIN SELECT RAISE(ABORT,'deployment execution input is retained'); END;
-- +goose StatementEnd
CREATE TABLE service_observations (
    environment_id TEXT NOT NULL,
    name TEXT NOT NULL,
    release_id TEXT NOT NULL,
    operation_id TEXT NOT NULL REFERENCES operations(id),
    image_digest TEXT NOT NULL,
    container_id TEXT NOT NULL CHECK(length(container_id)=64),
    state TEXT NOT NULL CHECK(state='running'),
    health TEXT NOT NULL CHECK(health='healthy'),
    observed_at INTEGER NOT NULL CHECK(observed_at>0),
    PRIMARY KEY(environment_id,name),
    FOREIGN KEY(environment_id,release_id) REFERENCES releases(environment_id,id),
    FOREIGN KEY(release_id,name) REFERENCES release_services(release_id,name)
);
-- +goose Down
DROP TABLE service_observations;
DROP TRIGGER deployment_execution_retained;
DROP TRIGGER deployment_execution_immutable;
DROP TABLE deployment_execution;
