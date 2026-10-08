-- name: ListProfiles :many
SELECT * FROM profiles ORDER BY is_default DESC, name;

-- name: GetProfile :one
SELECT * FROM profiles WHERE name = ?;

-- name: GetDefaultProfile :one
SELECT * FROM profiles WHERE is_default;

-- name: CreateProfile :one
INSERT INTO profiles (name, settings)
VALUES (?, ?)
ON CONFLICT (name) DO NOTHING
RETURNING *;

-- name: UpdateProfile :one
UPDATE profiles
SET settings = ?
WHERE name = ?
RETURNING *;

-- name: ClearDefaultProfile :exec
UPDATE profiles SET is_default = FALSE WHERE is_default;

-- name: SetDefaultProfile :execrows
UPDATE profiles SET is_default = TRUE WHERE name = ?;

-- name: DeleteProfile :execrows
DELETE FROM profiles WHERE name = ? AND NOT is_default;
