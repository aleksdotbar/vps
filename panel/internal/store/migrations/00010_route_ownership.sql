-- +goose Up
CREATE UNIQUE INDEX route_tunnel_identity ON server_tunnels(server_id,installation_id,account_id,tunnel_id);
CREATE UNIQUE INDEX route_zone_identity ON cloudflare_zones(id,name);
CREATE TABLE route_claims (
    hostname TEXT PRIMARY KEY NOT NULL CHECK(length(hostname) BETWEEN 1 AND 253),
    id TEXT NOT NULL UNIQUE,
    environment_id TEXT NOT NULL REFERENCES environments(id),
    server_id TEXT NOT NULL,
    installation_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    zone_id TEXT NOT NULL,
    zone_name TEXT NOT NULL,
    tunnel_id TEXT NOT NULL,
    credential_version INTEGER NOT NULL CHECK(credential_version>0),
    credential_kind TEXT NOT NULL DEFAULT 'credential' CHECK(credential_kind='credential'),
    credential_owner TEXT NOT NULL DEFAULT 'cloudflare-api' CHECK(credential_owner='cloudflare-api'),
    created_at INTEGER NOT NULL,
    FOREIGN KEY(server_id,installation_id,account_id,tunnel_id) REFERENCES server_tunnels(server_id,installation_id,account_id,tunnel_id),
    FOREIGN KEY(zone_id,zone_name) REFERENCES cloudflare_zones(id,name),
    FOREIGN KEY(credential_kind,credential_owner,credential_version) REFERENCES encrypted_versions(kind,owner_id,version)
);
CREATE INDEX route_claims_environment ON route_claims(environment_id,server_id);
CREATE INDEX route_claims_zone ON route_claims(zone_id);
CREATE TABLE release_routes (
    release_id TEXT NOT NULL REFERENCES releases(id),
    hostname TEXT NOT NULL REFERENCES route_claims(hostname),
    service TEXT NOT NULL CHECK(length(service) BETWEEN 1 AND 128),
    container_port INTEGER NOT NULL CHECK(container_port BETWEEN 1 AND 65535),
    PRIMARY KEY(release_id,hostname)
);
-- +goose StatementBegin
CREATE TRIGGER route_claim_immutable BEFORE UPDATE ON route_claims
BEGIN SELECT RAISE(ABORT,'route ownership is immutable'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER route_claim_retained BEFORE DELETE ON route_claims
BEGIN SELECT RAISE(ABORT,'route ownership is retained'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER release_route_identity BEFORE INSERT ON release_routes
WHEN NOT EXISTS(SELECT 1 FROM releases r JOIN route_claims c ON c.hostname=NEW.hostname
 WHERE r.id=NEW.release_id AND r.environment_id=c.environment_id AND r.server_id=c.server_id)
BEGIN SELECT RAISE(ABORT,'release route ownership conflicts'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER release_routes_immutable BEFORE UPDATE ON release_routes
BEGIN SELECT RAISE(ABORT,'release routes are immutable'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER release_routes_retained BEFORE DELETE ON release_routes
BEGIN SELECT RAISE(ABORT,'release route references are retained'); END;
-- +goose StatementEnd
-- +goose Down
DROP TRIGGER release_routes_retained;
DROP TRIGGER release_routes_immutable;
DROP TRIGGER release_route_identity;
DROP TRIGGER route_claim_retained;
DROP TRIGGER route_claim_immutable;
DROP TABLE release_routes;
DROP TABLE route_claims;
DROP INDEX route_zone_identity;
DROP INDEX route_tunnel_identity;
