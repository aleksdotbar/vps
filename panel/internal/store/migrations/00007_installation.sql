-- +goose Up
CREATE TABLE server_management (
    server_id TEXT PRIMARY KEY NOT NULL REFERENCES servers(id),
    credential_kind TEXT NOT NULL CHECK (credential_kind='credential'),
    credential_owner_id TEXT NOT NULL,
    credential_version INTEGER NOT NULL CHECK (credential_version>0),
    host_public_key TEXT NOT NULL,
    management_fingerprint TEXT NOT NULL,
    FOREIGN KEY (credential_kind,credential_owner_id,credential_version)
      REFERENCES encrypted_versions(kind,owner_id,version)
);
CREATE TABLE installation (
    id INTEGER PRIMARY KEY CHECK (id=1),
    installation_id TEXT NOT NULL UNIQUE,
    panel_server_id TEXT NOT NULL UNIQUE REFERENCES server_management(server_id),
    bootstrap_identity BLOB NOT NULL CHECK (length(bootstrap_identity)=32),
    private_proof_at INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);
-- +goose StatementBegin
CREATE TRIGGER protect_panel_host_delete BEFORE DELETE ON servers WHEN OLD.panel_host
BEGIN SELECT RAISE(ABORT,'panel host cannot be removed'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER protect_panel_host_identity BEFORE UPDATE OF id,panel_host ON servers
WHEN OLD.panel_host AND (NEW.id!=OLD.id OR NOT NEW.panel_host)
BEGIN SELECT RAISE(ABORT,'panel host identity is immutable'); END;
-- +goose StatementEnd
-- +goose Down
DROP TRIGGER protect_panel_host_identity;
DROP TRIGGER protect_panel_host_delete;
DROP TABLE installation;
DROP TABLE server_management;
