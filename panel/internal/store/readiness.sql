-- name: ListReadinessTargets :many
SELECT s.* FROM servers s
WHERE EXISTS (SELECT 1 FROM server_management m WHERE m.server_id=s.id)
ORDER BY s.observed_at,s.id LIMIT 4;
