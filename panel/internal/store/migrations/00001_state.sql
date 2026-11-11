-- +goose Up
CREATE TABLE administrator (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    password_hash TEXT NOT NULL,
    generation INTEGER NOT NULL CHECK (generation > 0),
    key_check BLOB NOT NULL
);
CREATE TABLE sessions (
    token TEXT PRIMARY KEY NOT NULL,
    data BLOB NOT NULL,
    expiry REAL NOT NULL
);
CREATE INDEX sessions_expiry ON sessions(expiry);
CREATE TABLE login_budget (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    window_start INTEGER NOT NULL,
    attempts INTEGER NOT NULL CHECK (attempts BETWEEN 1 AND 10)
);
CREATE TABLE encrypted_versions (
    kind TEXT NOT NULL CHECK (kind IN ('credential', 'environment')),
    owner_id TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    ciphertext BLOB NOT NULL,
    PRIMARY KEY (kind, owner_id, version)
);
-- +goose Down
DROP TABLE encrypted_versions;
DROP TABLE login_budget;
DROP TABLE sessions;
DROP TABLE administrator;
