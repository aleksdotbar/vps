-- name: GetCloudflareIntegration :one
SELECT * FROM cloudflare_integration WHERE id=1;
-- name: SetCloudflareIntegration :exec
INSERT INTO cloudflare_integration(id,account_id,token_id,version,expires_at,verified_at) VALUES(1,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET account_id=excluded.account_id,token_id=excluded.token_id,version=excluded.version,expires_at=excluded.expires_at,verified_at=excluded.verified_at;
-- name: ListCloudflareZones :many
SELECT id,name FROM cloudflare_zones ORDER BY id LIMIT 32;
-- name: UpsertCloudflareZone :exec
INSERT INTO cloudflare_zones(id,name) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name;
-- name: DeleteCloudflareZone :exec
DELETE FROM cloudflare_zones WHERE id=?;
