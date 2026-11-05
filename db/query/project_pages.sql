-- name: UpsertProjectPage :exec
INSERT INTO profile_pages (profile, url) VALUES (?, ?)
ON CONFLICT (profile) DO UPDATE SET url = excluded.url;

-- name: DeleteProjectPage :exec
DELETE FROM profile_pages WHERE profile = ?;

-- name: ListProjectPages :many
SELECT * FROM profile_pages;
