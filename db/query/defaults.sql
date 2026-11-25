-- name: ListDefaults :many
-- The defaults whose keys start with prefix, by key.
SELECT * FROM defaults
WHERE value IS NOT NULL
  AND substr(key, 1, length(CAST(sqlc.arg(prefix) AS TEXT))) = CAST(sqlc.arg(prefix) AS TEXT)
ORDER BY key;

-- name: GetDefault :one
-- The key's row, deleted (NULL value) or not.
SELECT * FROM defaults WHERE key = ?;

-- name: InsertDefault :one
INSERT INTO defaults (key, value) VALUES (?, ?)
RETURNING *;

-- name: UpdateDefault :one
-- A new version: updated_at is now, or a millisecond past the last if that
-- is no earlier, so each version has its own time. A NULL value deletes;
-- a deleted key made again is made now.
UPDATE defaults
SET value = sqlc.narg(value),
    version = version + 1,
    created_at = CASE WHEN value IS NULL THEN strftime('%Y-%m-%d %H:%M:%f', 'now') ELSE created_at END,
    updated_at = MAX(strftime('%Y-%m-%d %H:%M:%f', 'now'),
                     strftime('%Y-%m-%d %H:%M:%f', updated_at, '+0.001 seconds'))
WHERE key = sqlc.arg(key)
RETURNING *;
