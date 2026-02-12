-- name: GetSetting :one
SELECT value FROM settings
WHERE key = ?;

-- name: SetSetting :exec
INSERT INTO settings (key, value, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET
    value = excluded.value,
    updated_at = CURRENT_TIMESTAMP;

-- name: GetAllSettings :many
SELECT key, value FROM settings;
