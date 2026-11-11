-- name: GetOperationHistory :one
SELECT * FROM operation_history WHERE id=sqlc.arg(id);

-- name: ListOperationHistory :many
SELECT * FROM operation_history
WHERE (created_at,id)<(CAST(sqlc.arg(cursor_time) AS INTEGER),CAST(sqlc.arg(cursor_id) AS TEXT))
ORDER BY created_at DESC,id DESC LIMIT sqlc.arg(page_limit);

-- name: ListOperationHistoryForEnvironment :many
SELECT * FROM operation_history
WHERE environment_id=sqlc.arg(environment_id) AND (created_at,id)<(CAST(sqlc.arg(cursor_time) AS INTEGER),CAST(sqlc.arg(cursor_id) AS TEXT))
ORDER BY created_at DESC,id DESC LIMIT sqlc.arg(page_limit);

-- name: ListOperationHistoryForServer :many
SELECT * FROM operation_history
WHERE server_id=sqlc.arg(server_id) AND (created_at,id)<(CAST(sqlc.arg(cursor_time) AS INTEGER),CAST(sqlc.arg(cursor_id) AS TEXT))
ORDER BY created_at DESC,id DESC LIMIT sqlc.arg(page_limit);

-- name: ListOperationHistoryForEnvironmentServer :many
SELECT * FROM operation_history
WHERE environment_id=sqlc.arg(environment_id) AND server_id=sqlc.arg(server_id) AND (created_at,id)<(CAST(sqlc.arg(cursor_time) AS INTEGER),CAST(sqlc.arg(cursor_id) AS TEXT))
ORDER BY created_at DESC,id DESC LIMIT sqlc.arg(page_limit);
