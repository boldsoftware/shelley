-- Shelley archive store (server/archive_store.go): the store side.

-- name: UpsertArchiveStoreSource :exec
INSERT INTO archive_store_sources (source_vm, user_id, email) VALUES (?, ?, ?)
ON CONFLICT (source_vm) DO UPDATE SET
    user_id = excluded.user_id,
    email = excluded.email,
    last_push_at = CURRENT_TIMESTAMP;

-- name: SetArchiveStoreSourceProjects :exec
UPDATE archive_store_sources SET projects = ? WHERE source_vm = ?;

-- name: UpsertArchiveStoreConversation :exec
INSERT INTO archive_store_conversations (source_vm, conversation_id, parent_conversation_id,
    slug, project, model, cwd, archived, created_at, updated_at, have_seq)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (source_vm, conversation_id) DO UPDATE SET
    parent_conversation_id = excluded.parent_conversation_id,
    slug = excluded.slug,
    project = excluded.project,
    model = excluded.model,
    cwd = excluded.cwd,
    archived = excluded.archived,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at,
    have_seq = excluded.have_seq;

-- name: GetArchiveStoreHaveSeq :one
SELECT have_seq FROM archive_store_conversations WHERE source_vm = ? AND conversation_id = ?;

-- name: InsertArchiveStoreMessage :exec
INSERT INTO archive_store_messages (source_vm, conversation_id, sequence_id, message_id, type,
    llm_data, user_data, usage_data, display_data, created_at, generation,
    excluded_from_context, model_name, user_email, external_message_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (source_vm, conversation_id, sequence_id) DO NOTHING;

-- name: DeleteArchiveStoreMessages :exec
DELETE FROM archive_store_messages WHERE source_vm = ? AND conversation_id = ?;

-- name: DeleteArchiveStoreConversation :exec
DELETE FROM archive_store_conversations WHERE source_vm = ? AND conversation_id = ?;

-- name: ListArchiveStoreSources :many
SELECT m.*,
  CAST((SELECT COUNT(*) FROM archive_store_conversations c
        WHERE c.source_vm = m.source_vm AND c.parent_conversation_id IS NULL) AS INTEGER) AS conversation_count
FROM archive_store_sources m
ORDER BY m.email, m.source_vm;

-- name: ListArchiveStoreConversations :many
SELECT c.*, m.email,
  CAST((SELECT COUNT(*) FROM archive_store_messages t
        WHERE t.source_vm = c.source_vm AND t.conversation_id = c.conversation_id) AS INTEGER) AS message_count
FROM archive_store_conversations c
JOIN archive_store_sources m USING (source_vm)
ORDER BY c.updated_at DESC;

-- name: GetArchiveStoreConversation :one
SELECT c.*, m.email FROM archive_store_conversations c
JOIN archive_store_sources m USING (source_vm)
WHERE c.source_vm = ? AND c.conversation_id = ?;

-- name: ListArchiveStoreSubagents :many
SELECT conversation_id, slug FROM archive_store_conversations
WHERE source_vm = ? AND parent_conversation_id = ?
ORDER BY created_at;

-- name: ListArchiveStoreMessages :many
SELECT * FROM archive_store_messages
WHERE source_vm = ? AND conversation_id = ?
ORDER BY sequence_id;

-- name: SearchArchiveStoreMessages :many
-- The best-ranked hits, with snippets marked by the caller's sentinels.
SELECT m.source_vm, m.conversation_id,
  CAST(snippet(archive_store_messages_fts, 0, sqlc.arg(mark_start), sqlc.arg(mark_end), '...', 16) AS TEXT) AS snippet
FROM archive_store_messages_fts
JOIN archive_store_messages m ON m.id = archive_store_messages_fts.rowid
WHERE archive_store_messages_fts MATCH sqlc.arg(fts_match)
ORDER BY archive_store_messages_fts.rank
LIMIT 1000;
