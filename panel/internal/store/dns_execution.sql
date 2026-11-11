-- name: GetDNSRecord :one
SELECT * FROM dns_records WHERE claim_id=?;

-- name: ClaimDNSCreate :execrows
INSERT INTO dns_records(claim_id,operation_id,operation_fence,route_snapshot_sha256,credential_version,create_attempted_at)
VALUES(?,?,?,?,?,?) ON CONFLICT(claim_id) DO NOTHING;

-- name: BindDNSRecord :execrows
UPDATE dns_records SET record_id=?,observed_at=? WHERE claim_id=? AND (record_id IS NULL OR record_id=?);
