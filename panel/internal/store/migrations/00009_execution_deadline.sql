-- +goose Up
ALTER TABLE operations ADD COLUMN execution_deadline INTEGER NOT NULL DEFAULT 0 CHECK(execution_deadline>=0);
-- Existing started work cannot receive a fresh lifetime merely by upgrading.
UPDATE operations SET execution_deadline=created_at+1800000 WHERE started;
-- +goose StatementBegin
CREATE TRIGGER operation_deadline_immutable BEFORE UPDATE OF execution_deadline ON operations
WHEN OLD.execution_deadline != 0 AND NEW.execution_deadline != OLD.execution_deadline
BEGIN
    SELECT RAISE(ABORT,'operation execution deadline is immutable');
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER operation_deadline_immutable;
ALTER TABLE operations DROP COLUMN execution_deadline;
