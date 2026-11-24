-- Secrets (069-secrets.sql, 088-secret-expiry.sql). Expired secrets are
-- deleted before they're read.

-- name: CreateSecret :exec
-- lifetime is a SQLite date modifier, e.g. '+86400 seconds'.
INSERT INTO secrets (id, name, kind, sealed, conversation_id, expires_at)
VALUES (?, ?, ?, ?, ?, datetime('now', CAST(sqlc.arg(lifetime) AS TEXT)));

-- name: DeleteExpiredSecrets :execrows
DELETE FROM secrets WHERE expires_at <= CURRENT_TIMESTAMP;

-- name: ListSecrets :many
SELECT secrets.id, secrets.kind, secrets.conversation_id, secrets.expires_at, conversations.slug AS conversation_slug
FROM secrets LEFT JOIN conversations ON conversations.conversation_id = secrets.conversation_id
ORDER BY secrets.created_at DESC, secrets.id;

-- name: ListSealedSecrets :many
SELECT id, sealed, expires_at FROM secrets;

-- name: GetSealedSecret :one
SELECT sealed FROM secrets WHERE id = ?;

-- name: DeleteSecret :execrows
DELETE FROM secrets WHERE id = ?;
