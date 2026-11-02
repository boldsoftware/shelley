-- name: ListModelProviders :many
SELECT * FROM model_providers ORDER BY rowid;

-- name: GetModelProvider :one
SELECT * FROM model_providers WHERE provider_id = ?;

-- name: CreateModelProvider :exec
INSERT INTO model_providers (provider_id, provider_type, endpoint, api_key, display_name, models, version)
VALUES (?, ?, ?, ?, ?, ?, 1);

-- name: UpdateModelProvider :execrows
UPDATE model_providers
SET provider_type = ?, endpoint = ?, api_key = ?, display_name = ?, models = ?, version = version + 1
WHERE provider_id = ? AND version = ?;

-- name: DeleteModelProvider :execrows
DELETE FROM model_providers WHERE provider_id = ?;
