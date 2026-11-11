-- +goose Up
ALTER TABLE operations ADD COLUMN build_request_id TEXT REFERENCES waiting_builds(id)
    CHECK(build_request_id IS NULL OR build_request_id=id);
UPDATE operations SET build_request_id=id WHERE EXISTS(SELECT 1 FROM waiting_builds w WHERE w.id=operations.id);
-- +goose StatementBegin
CREATE TRIGGER operation_build_request_immutable BEFORE UPDATE OF build_request_id ON operations
WHEN NEW.build_request_id IS NOT OLD.build_request_id
BEGIN
    SELECT RAISE(ABORT,'operation build request is immutable');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER operation_build_request_required BEFORE INSERT ON operations
WHEN NEW.build_request_id IS NOT (SELECT id FROM waiting_builds WHERE id=NEW.id)
BEGIN
    SELECT RAISE(ABORT,'operation must retain its build request');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER waiting_build_before_operation BEFORE INSERT ON waiting_builds
WHEN EXISTS(SELECT 1 FROM operations WHERE id=NEW.id)
BEGIN
    SELECT RAISE(ABORT,'build request must precede operation');
END;
-- +goose StatementEnd
CREATE INDEX operations_history_created ON operations(created_at DESC,id DESC) WHERE build_request_id IS NULL;
CREATE INDEX operations_history_environment_created ON operations(environment_id,created_at DESC,id DESC) WHERE build_request_id IS NULL;
CREATE INDEX operations_history_server_created ON operations(server_id,created_at DESC,id DESC) WHERE build_request_id IS NULL;
CREATE INDEX operations_history_environment_server_created ON operations(environment_id,server_id,created_at DESC,id DESC) WHERE build_request_id IS NULL;
CREATE INDEX waiting_builds_history_created ON waiting_builds(created_at DESC,id DESC);
CREATE INDEX waiting_builds_history_environment_created ON waiting_builds(environment_id,created_at DESC,id DESC);
CREATE INDEX waiting_builds_history_server_created ON waiting_builds(server_id,created_at DESC,id DESC);
CREATE INDEX waiting_builds_history_environment_server_created ON waiting_builds(environment_id,server_id,created_at DESC,id DESC);
-- Build waiting and execution share one request identity. Preserve the original
-- admission order when a waiting build becomes executable.
CREATE VIEW operation_history AS
SELECT o.id,o.kind,o.state,o.reason,o.outcome,o.environment_id,o.server_id,
       o.release_id,o.created_at,o.updated_at,0 AS is_waiting
FROM operations o
WHERE o.build_request_id IS NULL
UNION ALL
SELECT w.id,CAST(COALESCE(o.kind,'deploy') AS TEXT),
       CAST(COALESCE(o.state,w.state) AS TEXT),
       CAST(COALESCE(o.reason,w.reason) AS TEXT),
       CAST(COALESCE(o.outcome,CASE w.state WHEN 'failed' THEN 'failure' WHEN 'needs_attention' THEN 'unknown' ELSE 'pending' END) AS TEXT),
       w.environment_id,w.server_id,CAST(COALESCE(o.release_id,'') AS TEXT),
       w.created_at,CAST(COALESCE(o.updated_at,w.updated_at) AS INTEGER),o.id IS NULL
FROM waiting_builds w LEFT JOIN operations o ON o.id=w.id;

-- +goose Down
DROP VIEW operation_history;
DROP INDEX operations_history_created;
DROP INDEX operations_history_environment_created;
DROP INDEX operations_history_server_created;
DROP INDEX operations_history_environment_server_created;
DROP INDEX waiting_builds_history_created;
DROP INDEX waiting_builds_history_environment_created;
DROP INDEX waiting_builds_history_server_created;
DROP INDEX waiting_builds_history_environment_server_created;
DROP TRIGGER waiting_build_before_operation;
DROP TRIGGER operation_build_request_required;
DROP TRIGGER operation_build_request_immutable;
ALTER TABLE operations DROP COLUMN build_request_id;
