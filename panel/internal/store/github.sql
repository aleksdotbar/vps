-- name: GetGitHubIntegration :one
SELECT * FROM github_integration WHERE id=1;
-- name: CreateGitHubIntegration :exec
INSERT INTO github_integration(id,app_id,owner_id,owner_login,app_slug,credential_version) VALUES(1,?,?,?,?,?);
-- name: SetGitHubInstallation :exec
UPDATE github_integration SET installation_id=? WHERE id=1;
-- name: PruneGitHubSetupStates :exec
DELETE FROM github_setup_states WHERE expires_at<? OR consumed;
-- name: GitHubSetupStateCount :one
SELECT count(*) FROM github_setup_states;
-- name: CreateGitHubSetupState :exec
INSERT INTO github_setup_states(state_hash,phase,owner_id,owner_login,session_hash,generation,expires_at) VALUES(?,?,?,?,?,?,?);
-- name: GetGitHubSetupState :one
SELECT * FROM github_setup_states WHERE state_hash=?;
-- name: ConsumeGitHubSetupState :execrows
UPDATE github_setup_states SET consumed=TRUE WHERE state_hash=? AND NOT consumed AND expires_at>?;
-- name: CreateGitHubRepository :exec
INSERT INTO github_repositories(id,owner,name) VALUES(?,?,?);
-- name: GetGitHubRepository :one
SELECT * FROM github_repositories WHERE id=?;
-- name: ListGitHubRepositories :many
SELECT * FROM github_repositories WHERE id>? ORDER BY id LIMIT ?;
-- name: ClearGitHubRepositorySelection :exec
UPDATE github_repositories SET selected=FALSE;
-- name: SelectGitHubRepository :exec
UPDATE github_repositories SET selected=TRUE WHERE id=?;
-- name: GetRegistryCredential :one
SELECT * FROM registry_credential WHERE id=1;
-- name: SetRegistryCredential :exec
INSERT INTO registry_credential(id,username,version) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET username=excluded.username,version=excluded.version;
-- name: ClearGitHubRepositories :exec
DELETE FROM github_repositories;
-- name: UpdateProjectRepositoryName :exec
UPDATE projects SET repository_owner=?,repository_name=? WHERE repository_id=?;
-- name: ClearSelectedGitHubRepository :exec
UPDATE github_repositories SET selected=FALSE WHERE id=?;
