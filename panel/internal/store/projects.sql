-- name: GetServer :one
SELECT * FROM servers WHERE id=?;
-- name: CreateServer :exec
INSERT INTO servers(id,name,tailscale_address,ssh_fingerprint,panel_host,created_at) VALUES(?,?,?,?,?,?);
-- name: ObserveServer :exec
UPDATE servers SET readiness=?,observed_at=? WHERE id=?;
-- name: ListServers :many
SELECT * FROM servers WHERE (created_at,id)<(sqlc.arg(cursor_time),CAST(sqlc.arg(cursor_id) AS TEXT)) ORDER BY created_at DESC,id DESC LIMIT sqlc.arg(page_limit);
-- name: GetProject :one
SELECT * FROM projects WHERE id=?;
-- name: CreateProject :exec
INSERT INTO projects(id,name,repository_id,repository_owner,repository_name,created_at) VALUES(?,?,?,?,?,?);
-- name: ListProjects :many
SELECT * FROM projects WHERE (created_at,id)<(sqlc.arg(cursor_time),CAST(sqlc.arg(cursor_id) AS TEXT)) ORDER BY created_at DESC,id DESC LIMIT sqlc.arg(page_limit);
-- name: GetEnvironment :one
SELECT * FROM environments WHERE id=?;
-- name: CreateEnvironment :exec
INSERT INTO environments(id,project_id,name,server_id,branch,override_path,domains_json,auto_deploy,version,binding_version,secret_version,secret_names_json,bridge_name,created_at) VALUES(?,?,?,?,?,?,?,?,1,1,1,?,?,?);
-- name: UpdateEnvironment :exec
UPDATE environments SET name=?,server_id=?,branch=?,override_path=?,domains_json=?,auto_deploy=?,version=version+1,binding_version=? WHERE id=?;
-- name: UpdateEnvironmentSecrets :exec
UPDATE environments SET version=version+1,secret_version=?,secret_names_json=? WHERE id=?;
-- name: ListEnvironments :many
SELECT * FROM environments WHERE project_id=sqlc.arg(project_id) AND (created_at,id)<(sqlc.arg(cursor_time),CAST(sqlc.arg(cursor_id) AS TEXT)) ORDER BY created_at DESC,id DESC LIMIT sqlc.arg(page_limit);
-- name: DeleteDesiredDomains :exec
DELETE FROM desired_domains WHERE environment_id=?;
-- name: ReserveDesiredDomain :exec
INSERT INTO desired_domains(hostname,environment_id) VALUES(?,?);
-- name: StartedEnvironmentOperationCount :one
SELECT count(*) FROM operations WHERE environment_id=? AND started AND state IN ('running','recovering','needs_attention');
-- name: SupersedeOldBindingOperations :exec
UPDATE operations SET state='superseded',reason='binding_changed',updated_at=? WHERE environment_id=? AND binding_version!=? AND state='queued' AND NOT started;
-- name: MetadataCapacity :one
SELECT (SELECT count(*) FROM projects) AS projects,(SELECT count(*) FROM environments) AS environments,(SELECT count(*) FROM metadata_mutations) AS mutations;
-- name: GetMetadataMutation :one
SELECT * FROM metadata_mutations WHERE request_id=?;
-- name: CreateMetadataMutation :exec
INSERT INTO metadata_mutations(request_id,procedure,identity,encrypted_response,created_at) VALUES(?,?,?,?,?);
-- name: CreateRelease :exec
INSERT INTO releases(id,environment_id,server_id,repository_id,branch,source_sha,environment_version,binding_version,images_json,domains_json,secret_version,compose_config,secret_bindings_json,config_sha256,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?);
-- name: GetRelease :one
SELECT * FROM releases WHERE id=?;
-- name: ListReleases :many
SELECT id,environment_id,server_id,repository_id,branch,source_sha,environment_version,binding_version,images_json,domains_json,secret_version,config_sha256,created_at FROM releases WHERE environment_id=sqlc.arg(environment_id) AND (created_at,id)<(sqlc.arg(cursor_time),CAST(sqlc.arg(cursor_id) AS TEXT)) ORDER BY created_at DESC,id DESC LIMIT sqlc.arg(page_limit);
-- name: CreateReleaseService :exec
INSERT INTO release_services(release_id,name,image_digest) VALUES(?,?,?);
-- name: ListReleaseServices :many
SELECT * FROM release_services WHERE release_id=? AND name>? ORDER BY name LIMIT ?;

-- name: GetReleaseMetadata :one
SELECT id,environment_id,server_id,repository_id,branch,source_sha,environment_version,binding_version,images_json,domains_json,secret_version,config_sha256,created_at FROM releases WHERE id=?;
-- name: GetLatestRelease :one
SELECT id,environment_id,server_id,repository_id,branch,source_sha,environment_version,binding_version,images_json,domains_json,secret_version,config_sha256,created_at FROM releases WHERE environment_id=? ORDER BY created_at DESC,id DESC LIMIT 1;
-- name: SupersedeQueuedAutomaticOperations :exec
UPDATE operations SET state='superseded',reason='automatic_deploy_disabled',updated_at=? WHERE environment_id=? AND automatic AND state='queued' AND NOT started;
