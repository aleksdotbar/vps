-- +goose Up
-- Operator-visible failure detail reported by the host after a failed candidate.
CREATE TABLE operation_diagnostics (
    operation_id TEXT PRIMARY KEY NOT NULL REFERENCES operations(id),
    diagnostics_json BLOB NOT NULL CHECK (length(diagnostics_json) <= 32768),
    recorded_at INTEGER NOT NULL CHECK (recorded_at > 0)
);
-- +goose Down
DROP TABLE operation_diagnostics;
