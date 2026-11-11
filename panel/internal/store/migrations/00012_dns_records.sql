-- +goose Up
CREATE TABLE dns_records (
    claim_id TEXT PRIMARY KEY NOT NULL REFERENCES route_claims(id),
    operation_id TEXT NOT NULL REFERENCES operations(id),
    operation_fence INTEGER NOT NULL CHECK(operation_fence>0),
    route_snapshot_sha256 TEXT NOT NULL CHECK(length(route_snapshot_sha256)=64),
    credential_version INTEGER NOT NULL CHECK(credential_version>0),
    credential_kind TEXT NOT NULL DEFAULT 'credential' CHECK(credential_kind='credential'),
    credential_owner TEXT NOT NULL DEFAULT 'cloudflare-api' CHECK(credential_owner='cloudflare-api'),
    create_attempted_at INTEGER NOT NULL CHECK(create_attempted_at>0),
    record_id TEXT UNIQUE CHECK(record_id IS NULL OR length(record_id)=32),
    observed_at INTEGER NOT NULL DEFAULT 0 CHECK(observed_at>=0),
    FOREIGN KEY(credential_kind,credential_owner,credential_version) REFERENCES encrypted_versions(kind,owner_id,version),
    CHECK(observed_at=0 OR record_id IS NOT NULL)
);
-- The row itself is the irreversible create-attempt claim. It is inserted and
-- committed before POST; neither a new job nor a token rotation can reset it.
-- +goose StatementBegin
CREATE TRIGGER dns_attempt_immutable BEFORE UPDATE ON dns_records
WHEN NEW.claim_id!=OLD.claim_id OR NEW.operation_id!=OLD.operation_id
 OR NEW.operation_fence!=OLD.operation_fence OR NEW.route_snapshot_sha256!=OLD.route_snapshot_sha256
 OR NEW.credential_version!=OLD.credential_version OR NEW.credential_kind!=OLD.credential_kind
 OR NEW.credential_owner!=OLD.credential_owner OR NEW.create_attempted_at!=OLD.create_attempted_at
 OR (OLD.record_id IS NOT NULL AND NEW.record_id IS NOT OLD.record_id)
BEGIN SELECT RAISE(ABORT,'DNS create identity is immutable'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER dns_attempt_retained BEFORE DELETE ON dns_records
BEGIN SELECT RAISE(ABORT,'DNS create attempt is retained'); END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER dns_attempt_retained;
DROP TRIGGER dns_attempt_immutable;
DROP TABLE dns_records;
