-- +goose Up
CREATE UNIQUE INDEX cloudflare_account_identity ON cloudflare_integration(account_id);
CREATE TABLE server_tunnels (
    server_id TEXT PRIMARY KEY NOT NULL REFERENCES server_management(server_id),
    request_id TEXT NOT NULL UNIQUE,
    installation_id TEXT NOT NULL REFERENCES installation(installation_id),
    account_id TEXT NOT NULL REFERENCES cloudflare_integration(account_id),
    credential_version INTEGER NOT NULL CHECK(credential_version>0),
    credential_kind TEXT NOT NULL DEFAULT 'credential' CHECK(credential_kind='credential'),
    credential_owner TEXT NOT NULL DEFAULT 'cloudflare-api' CHECK(credential_owner='cloudflare-api'),
    name TEXT NOT NULL UNIQUE,
    phase TEXT NOT NULL CHECK(phase IN ('queued','prepared','creating','finalizing','ready','needs_attention')),
    create_attempted BOOLEAN NOT NULL DEFAULT FALSE,
    create_attempted_at INTEGER NOT NULL DEFAULT 0,
    tunnel_id TEXT UNIQUE,
    config_sha256 TEXT NOT NULL DEFAULT '',
    observed_at INTEGER NOT NULL DEFAULT 0,
    reason TEXT NOT NULL DEFAULT '',
    polls INTEGER NOT NULL DEFAULT 0 CHECK(polls BETWEEN 0 AND 120),
    river_job_id INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    FOREIGN KEY(credential_kind,credential_owner,credential_version) REFERENCES encrypted_versions(kind,owner_id,version),
    CHECK(tunnel_id IS NULL OR create_attempted),
    CHECK(phase!='ready' OR (tunnel_id IS NOT NULL AND length(config_sha256)=64 AND observed_at>0))
);
-- +goose StatementBegin
CREATE TRIGGER tunnel_attempt_is_one_way BEFORE UPDATE OF create_attempted ON server_tunnels
WHEN OLD.create_attempted AND NOT NEW.create_attempted
BEGIN SELECT RAISE(ABORT,'Tunnel create attempt cannot be reset'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER tunnel_management_identity BEFORE UPDATE ON server_management
WHEN EXISTS(SELECT 1 FROM server_tunnels WHERE server_id=OLD.server_id)
AND (NEW.server_id!=OLD.server_id OR NEW.credential_kind!=OLD.credential_kind
 OR NEW.credential_owner_id!=OLD.credential_owner_id OR NEW.credential_version!=OLD.credential_version
 OR NEW.host_public_key!=OLD.host_public_key OR NEW.management_fingerprint!=OLD.management_fingerprint)
BEGIN SELECT RAISE(ABORT,'Tunnel management identity is bound'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER tunnel_host_identity BEFORE UPDATE ON servers
WHEN EXISTS(SELECT 1 FROM server_tunnels WHERE server_id=OLD.id)
AND (NEW.id!=OLD.id OR NEW.tailscale_address!=OLD.tailscale_address OR NEW.ssh_fingerprint!=OLD.ssh_fingerprint)
BEGIN SELECT RAISE(ABORT,'Tunnel host identity is bound'); END;
-- +goose StatementEnd
-- +goose Down
DROP TRIGGER tunnel_host_identity;
DROP TRIGGER tunnel_management_identity;
DROP TRIGGER tunnel_attempt_is_one_way;
DROP TABLE server_tunnels;
DROP INDEX cloudflare_account_identity;
