-- name: GetRouteClaim :one
SELECT * FROM route_claims WHERE hostname=?;
-- name: RouteClaimCount :one
SELECT count(*) FROM route_claims;
-- name: EnvironmentRouteClaimsOnOtherServer :one
SELECT count(*) FROM route_claims WHERE environment_id=? AND server_id!=?;
-- name: CreateRouteClaim :exec
INSERT INTO route_claims(hostname,id,environment_id,server_id,installation_id,account_id,zone_id,zone_name,tunnel_id,credential_version,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?);
-- name: GetDesiredDomainOwner :one
SELECT environment_id FROM desired_domains WHERE hostname=?;
-- name: CreateReleaseRoute :exec
INSERT INTO release_routes(release_id,hostname,service,container_port) VALUES(?,?,?,?);
-- name: ListReleaseRoutes :many
SELECT r.release_id,r.hostname,r.service,r.container_port,c.id AS claim_id,c.environment_id,c.server_id,c.installation_id,c.account_id,c.zone_id,c.zone_name,c.tunnel_id,c.credential_version
FROM release_routes r JOIN route_claims c ON c.hostname=r.hostname WHERE r.release_id=? ORDER BY r.hostname LIMIT 33;
-- name: ListClaimedCloudflareZones :many
SELECT DISTINCT zone_id,zone_name FROM route_claims ORDER BY zone_id LIMIT 33;
