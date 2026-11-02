-- name: GetCustomization :one
SELECT sqlc.embed(t), c.archived
FROM shelley_customizations t JOIN conversations c ON c.conversation_id = t.conversation_id
WHERE t.conversation_id = ?;

-- name: ListCustomizations :many
SELECT sqlc.embed(t), c.archived
FROM shelley_customizations t JOIN conversations c ON c.conversation_id = t.conversation_id
ORDER BY t.conversation_id;

-- name: HasVerifyingCustomization :one
SELECT CAST(EXISTS (SELECT 1 FROM shelley_customizations WHERE status = 'verifying') AS BOOLEAN);

-- name: HasActiveCustomization :one
-- Whether a customization is verifying or restarting.
SELECT CAST(EXISTS (SELECT 1 FROM shelley_customizations WHERE status IN ('verifying', 'restarting')) AS BOOLEAN);

-- name: UpsertCustomization :exec
INSERT INTO shelley_customizations (conversation_id, status, generation, artifact_path, data)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (conversation_id) DO UPDATE SET
    status = excluded.status, generation = excluded.generation,
    artifact_path = excluded.artifact_path, data = excluded.data;

-- name: CreateCustomizationAutoRestart :exec
INSERT INTO shelley_customization_auto_restarts (conversation_id, commit_sha, artifact_sha256, event, error, created_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: LastCustomizationAutoRestart :one
SELECT event FROM shelley_customization_auto_restarts
WHERE artifact_sha256 = ? ORDER BY id DESC LIMIT 1;
