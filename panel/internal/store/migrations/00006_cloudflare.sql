-- +goose Up
CREATE TABLE cloudflare_integration (
    id INTEGER PRIMARY KEY CHECK(id=1),
    account_id TEXT NOT NULL CHECK(length(account_id)=32),
    token_id TEXT NOT NULL CHECK(length(token_id)=32),
    version INTEGER NOT NULL CHECK(version>0),
    expires_at INTEGER,
    verified_at INTEGER NOT NULL,
    credential_kind TEXT NOT NULL DEFAULT 'credential' CHECK(credential_kind='credential'),
    credential_owner TEXT NOT NULL DEFAULT 'cloudflare-api' CHECK(credential_owner='cloudflare-api'),
    FOREIGN KEY(credential_kind,credential_owner,version) REFERENCES encrypted_versions(kind,owner_id,version)
);
CREATE TABLE cloudflare_zones (
    id TEXT PRIMARY KEY NOT NULL CHECK(length(id)=32),
    integration_id INTEGER NOT NULL DEFAULT 1 CHECK(integration_id=1) REFERENCES cloudflare_integration(id),
    name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 253),
    UNIQUE(name)
);
-- +goose Down
DROP TABLE cloudflare_zones;
DROP TABLE cloudflare_integration;
