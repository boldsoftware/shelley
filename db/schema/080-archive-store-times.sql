-- The archive store kept pushed times in Go's text form ("2006-01-02
-- 15:04:05.999999999 +0000 UTC"), which SQLite's date functions don't
-- read. It now writes SQLite's ("2006-01-02 15:04:05", UTC, like Shelley's
-- own tables); this converts what it has. Pushed times are always UTC.
UPDATE archive_store_conversations SET created_at = substr(created_at, 1, 19)
WHERE created_at LIKE '% +0000 UTC';
UPDATE archive_store_conversations SET updated_at = substr(updated_at, 1, 19)
WHERE updated_at LIKE '% +0000 UTC';
UPDATE archive_store_messages SET created_at = substr(created_at, 1, 19)
WHERE created_at LIKE '% +0000 UTC';
