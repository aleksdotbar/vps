-- name: GetInstallation :one
SELECT * FROM installation WHERE id=1;
-- name: CreateInstallation :exec
INSERT INTO installation(id,installation_id,panel_server_id,operation_id,input_sha256,source_system,enrollment_system,bootstrap_identity,created_at) VALUES(1,?,?,?,?,?,?,?,?);
-- name: GetServerManagement :one
SELECT * FROM server_management WHERE server_id=?;
-- name: CreateServerManagement :exec
INSERT INTO server_management(server_id,credential_kind,credential_owner_id,credential_version,host_public_key,management_fingerprint) VALUES(?,?,?,?,?,?);
-- name: RecordPanelPrivateProof :exec
UPDATE installation SET private_proof_at=? WHERE id=1;
