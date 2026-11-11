-- +goose Up
CREATE INDEX servers_readiness_order ON servers(observed_at,id);
-- +goose Down
DROP INDEX servers_readiness_order;
