-- name: ListEmbedOrigins :many
SELECT origin FROM embed_origins ORDER BY origin;

-- name: AddEmbedOrigin :exec
INSERT INTO embed_origins (origin) VALUES (?) ON CONFLICT DO NOTHING;

-- name: DeleteEmbedOrigin :exec
DELETE FROM embed_origins WHERE origin = ?;
