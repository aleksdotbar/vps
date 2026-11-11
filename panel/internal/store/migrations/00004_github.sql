-- +goose Up
CREATE TABLE github_setup_states (
    state_hash BLOB PRIMARY KEY NOT NULL CHECK(length(state_hash)=32),
    phase TEXT NOT NULL CHECK(phase IN ('manifest','installation')),
    owner_id INTEGER NOT NULL CHECK(owner_id>0),
    owner_login TEXT NOT NULL,
    session_hash BLOB NOT NULL CHECK(length(session_hash)=32),
    generation INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    consumed BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE TABLE github_integration (
    id INTEGER PRIMARY KEY CHECK(id=1),
    app_id INTEGER NOT NULL CHECK(app_id>0),
    owner_id INTEGER NOT NULL CHECK(owner_id>0),
    owner_login TEXT NOT NULL,
    app_slug TEXT NOT NULL,
    credential_version INTEGER NOT NULL,
    installation_id INTEGER NOT NULL DEFAULT 0,
    credential_kind TEXT NOT NULL DEFAULT 'credential' CHECK(credential_kind='credential'),
    credential_owner TEXT NOT NULL DEFAULT 'github-app' CHECK(credential_owner='github-app'),
    FOREIGN KEY(credential_kind,credential_owner,credential_version) REFERENCES encrypted_versions(kind,owner_id,version)
);
CREATE TABLE github_repositories (
    id INTEGER PRIMARY KEY NOT NULL,
    owner TEXT NOT NULL,
    name TEXT NOT NULL,
    selected BOOLEAN NOT NULL DEFAULT TRUE
);
CREATE TABLE registry_credential (
    id INTEGER PRIMARY KEY CHECK(id=1),
    username TEXT NOT NULL,
    version INTEGER NOT NULL,
    credential_kind TEXT NOT NULL DEFAULT 'credential' CHECK(credential_kind='credential'),
    credential_owner TEXT NOT NULL DEFAULT 'ghcr-read' CHECK(credential_owner='ghcr-read'),
    FOREIGN KEY(credential_kind,credential_owner,version) REFERENCES encrypted_versions(kind,owner_id,version)
);
-- +goose Down
DROP TABLE registry_credential;
DROP TABLE github_repositories;
DROP TABLE github_integration;
DROP TABLE github_setup_states;
