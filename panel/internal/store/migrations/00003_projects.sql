-- +goose Up
CREATE TABLE servers (
    id TEXT PRIMARY KEY NOT NULL,
    name TEXT NOT NULL,
    tailscale_address TEXT NOT NULL UNIQUE,
    ssh_fingerprint TEXT NOT NULL,
    panel_host BOOLEAN NOT NULL DEFAULT FALSE,
    readiness TEXT NOT NULL DEFAULT 'setup_pending' CHECK (readiness IN ('setup_pending','ready','unreachable','needs_attention')),
    observed_at INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX servers_one_panel_host ON servers(panel_host) WHERE panel_host;
CREATE INDEX servers_created ON servers(created_at DESC,id DESC);
CREATE TABLE projects (
    id TEXT PRIMARY KEY NOT NULL,
    name TEXT NOT NULL,
    repository_id INTEGER NOT NULL UNIQUE CHECK (repository_id>0),
    repository_owner TEXT NOT NULL,
    repository_name TEXT NOT NULL,
    workflow_id INTEGER NOT NULL CHECK (workflow_id>0),
    created_at INTEGER NOT NULL
);
CREATE INDEX projects_created ON projects(created_at DESC,id DESC);
CREATE TABLE environments (
    id TEXT PRIMARY KEY NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id),
    name TEXT NOT NULL,
    server_id TEXT NOT NULL REFERENCES servers(id),
    branch TEXT NOT NULL,
    override_path TEXT NOT NULL,
    domains_json BLOB NOT NULL,
    auto_deploy BOOLEAN NOT NULL,
    version INTEGER NOT NULL CHECK(version>0),
    binding_version INTEGER NOT NULL CHECK(binding_version>0),
    applied_version INTEGER NOT NULL DEFAULT 0 CHECK(applied_version>=0),
    secret_kind TEXT NOT NULL DEFAULT 'environment' CHECK(secret_kind='environment'),
    secret_version INTEGER NOT NULL CHECK(secret_version>0),
    secret_names_json BLOB NOT NULL,
    bridge_name TEXT NOT NULL UNIQUE,
    current_release_id TEXT,
    previous_release_id TEXT,
    created_at INTEGER NOT NULL,
    UNIQUE(project_id,name),
    FOREIGN KEY(secret_kind,id,secret_version) REFERENCES encrypted_versions(kind,owner_id,version),
    FOREIGN KEY(id,current_release_id) REFERENCES releases(environment_id,id) DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(id,previous_release_id) REFERENCES releases(environment_id,id) DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX environments_project_created ON environments(project_id,created_at DESC,id DESC);
CREATE INDEX environments_server ON environments(server_id);
CREATE TABLE desired_domains (
    hostname TEXT PRIMARY KEY NOT NULL,
    environment_id TEXT NOT NULL REFERENCES environments(id)
);
CREATE TABLE releases (
    id TEXT PRIMARY KEY NOT NULL,
    environment_id TEXT NOT NULL REFERENCES environments(id),
    server_id TEXT NOT NULL REFERENCES servers(id),
    repository_id INTEGER NOT NULL,
    workflow_id INTEGER NOT NULL,
    run_id INTEGER NOT NULL,
    run_attempt INTEGER NOT NULL,
    branch TEXT NOT NULL,
    source_sha TEXT NOT NULL,
    environment_version INTEGER NOT NULL,
    binding_version INTEGER NOT NULL,
    images_json BLOB NOT NULL,
    domains_json BLOB NOT NULL,
    secret_kind TEXT NOT NULL DEFAULT 'environment' CHECK(secret_kind='environment'),
    secret_version INTEGER NOT NULL,
    compose_config BLOB NOT NULL CHECK(length(compose_config)<=2097152),
    secret_bindings_json BLOB NOT NULL,
    config_sha256 TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    UNIQUE(environment_id,id),
    FOREIGN KEY(secret_kind,environment_id,secret_version) REFERENCES encrypted_versions(kind,owner_id,version)
);
CREATE INDEX releases_environment_created ON releases(environment_id,created_at DESC,id DESC);
CREATE TABLE release_services (
    release_id TEXT NOT NULL REFERENCES releases(id),
    name TEXT NOT NULL,
    image_digest TEXT NOT NULL,
    PRIMARY KEY(release_id,name)
);
CREATE TABLE metadata_mutations (
    request_id TEXT PRIMARY KEY NOT NULL,
    procedure TEXT NOT NULL,
    identity BLOB NOT NULL CHECK(length(identity)=32),
    encrypted_response BLOB NOT NULL,
    created_at INTEGER NOT NULL
);
-- +goose Down
UPDATE environments SET current_release_id=NULL,previous_release_id=NULL;
DROP TABLE metadata_mutations;
DROP TABLE release_services;
DROP TABLE desired_domains;
DROP TABLE releases;
DROP TABLE environments;
DROP TABLE projects;
DROP TABLE servers;
