-- The archiver (server/archiver.go): what this Shelley pushes to an archive store.

-- name: ListArchiverPushes :many
-- Every recorded conversation that still exists, with what the push needs
-- to decide on it: its row, its root's, and its highest sequence id.
SELECT p.conversation_id, p.pushed_seq, p.pushed_meta,
  c.slug, c.parent_conversation_id, c.model, c.cwd, c.archived, c.is_draft,
  c.created_at, c.updated_at, c.conversation_options,
  CAST(COALESCE(r.conversation_options, '{}') AS TEXT) AS root_options,
  CAST(r.external_conversation_id IS NOT NULL AS BOOLEAN) AS root_channel,
  CAST(COALESCE((SELECT MAX(m.sequence_id) FROM messages m WHERE m.conversation_id = p.conversation_id), 0) AS INTEGER) AS max_seq
FROM archiver_pushes p
JOIN conversations c ON c.conversation_id = p.conversation_id
LEFT JOIN conversations r ON r.conversation_id = p.root_id
ORDER BY p.rowid;

-- name: ListArchiverDeleted :many
-- Recorded conversations that were deleted, and whether the store has them.
SELECT conversation_id, CAST(pushed_meta <> '' AS BOOLEAN) AS pushed FROM archiver_pushes p
WHERE NOT EXISTS (SELECT 1 FROM conversations c WHERE c.conversation_id = p.conversation_id);

-- name: ListArchiverMessagesAfter :many
-- With whether each is temporary (db/schema/082-temporary-messages.sql).
SELECT sqlc.embed(m),
  CAST(EXISTS (SELECT 1 FROM temporary_messages t WHERE t.message_id = m.message_id) AS BOOLEAN) AS is_temporary
FROM messages m
WHERE m.conversation_id = ? AND m.sequence_id > ?
ORDER BY m.sequence_id
LIMIT ?;

-- name: SetArchiverPushed :exec
UPDATE archiver_pushes SET pushed_seq = ?, pushed_meta = ? WHERE conversation_id = ?;

-- name: DeleteArchiverPush :exec
DELETE FROM archiver_pushes WHERE conversation_id = ?;

-- name: GetArchiverProjects :one
SELECT projects FROM archiver_projects WHERE singleton = 1;

-- name: SetArchiverProjects :exec
INSERT INTO archiver_projects (singleton, projects) VALUES (1, ?)
ON CONFLICT (singleton) DO UPDATE SET projects = excluded.projects;
