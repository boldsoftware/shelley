-- Secrets (069-secrets.sql).

-- name: CreateSecret :exec
INSERT INTO secrets (id, name, kind, sealed, conversation_id) VALUES (?, ?, ?, ?, ?);

-- name: ListSecrets :many
SELECT secrets.id, secrets.name, secrets.kind, secrets.conversation_id, secrets.created_at, conversations.slug AS conversation_slug
FROM secrets LEFT JOIN conversations ON conversations.conversation_id = secrets.conversation_id
ORDER BY secrets.created_at DESC, secrets.id;

-- name: ListSealedSecrets :many
SELECT id, sealed FROM secrets;

-- name: GetSealedSecret :one
SELECT sealed FROM secrets WHERE id = ?;

-- name: RenameSecret :execrows
UPDATE secrets SET name = ? WHERE id = ?;

-- name: DeleteSecret :execrows
DELETE FROM secrets WHERE id = ?;

-- name: RecordSecretInjection :exec
INSERT INTO secret_injections (secret_id, conversation_id, machine, path) VALUES (?, ?, ?, ?);

-- name: ListSecretInjections :many
SELECT secret_id, conversation_id, machine, path, created_at FROM secret_injections ORDER BY created_at DESC, id DESC;
