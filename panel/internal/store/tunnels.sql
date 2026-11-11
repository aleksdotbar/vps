-- name: GetServerTunnel :one
SELECT * FROM server_tunnels WHERE server_id=?;
-- name: CloudflareTunnelCapacity :one
SELECT count(*) AS total,count(*) FILTER(WHERE phase NOT IN ('ready','needs_attention')) AS unresolved FROM server_tunnels;
-- name: CloudflareAccountTunnelCount :one
SELECT count(*) FROM server_tunnels WHERE account_id=?;
-- name: CreateServerTunnel :exec
INSERT INTO server_tunnels(server_id,request_id,installation_id,account_id,credential_version,name,phase,created_at,updated_at) VALUES(?,?,?,?,?,?,'queued',?,?);
-- name: SetTunnelJob :exec
UPDATE server_tunnels SET river_job_id=? WHERE server_id=?;
-- name: PrepareServerTunnel :exec
UPDATE server_tunnels SET phase='prepared',updated_at=? WHERE server_id=? AND NOT create_attempted;
-- name: ClaimTunnelCreate :execrows
UPDATE server_tunnels SET create_attempted=TRUE,create_attempted_at=?,phase='creating',updated_at=? WHERE server_id=? AND NOT create_attempted AND tunnel_id IS NULL;
-- name: BindCreatedTunnel :execrows
UPDATE server_tunnels SET tunnel_id=?,phase='finalizing',updated_at=? WHERE server_id=? AND create_attempted AND (tunnel_id IS NULL OR tunnel_id=?);
-- name: ObserveServerTunnel :exec
UPDATE server_tunnels SET phase=?,config_sha256=?,observed_at=?,reason='',updated_at=? WHERE server_id=?;
-- name: AttendServerTunnel :exec
UPDATE server_tunnels SET phase='needs_attention',reason=?,updated_at=? WHERE server_id=?;
-- name: AdvanceTunnelPoll :execrows
UPDATE server_tunnels SET polls=polls+1,updated_at=? WHERE server_id=? AND polls<120;
-- name: ResumeServerTunnel :execrows
UPDATE server_tunnels SET phase=CASE WHEN tunnel_id IS NOT NULL THEN 'finalizing' WHEN create_attempted THEN 'creating' ELSE 'queued' END,polls=0,reason='',updated_at=? WHERE server_id=? AND river_job_id=? AND phase!='ready';
