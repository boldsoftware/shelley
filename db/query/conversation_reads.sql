-- name: ConversationAttention :many
SELECT conversation_id, marked_at FROM conversation_marks
WHERE kind = 'reply' AND user_id = '' AND conversation_id IN (sqlc.slice('conversation_ids'));

-- name: ClearConversationAttention :exec
DELETE FROM conversation_marks
WHERE kind = 'reply' AND user_id = '' AND conversation_id = ?;

-- name: UpsertConversationRead :one
INSERT INTO conversation_marks (kind, user_id, conversation_id, marked_at)
SELECT 'read', sqlc.arg(user_id), reply.conversation_id, CAST(sqlc.arg(read_at) AS INTEGER)
FROM conversation_marks AS reply
WHERE reply.kind = 'reply' AND reply.user_id = '' AND reply.conversation_id = sqlc.arg(conversation_id)
  AND reply.marked_at >= sqlc.arg(read_at)
ON CONFLICT (kind, user_id, conversation_id) DO UPDATE SET marked_at = MAX(marked_at, excluded.marked_at)
RETURNING marked_at;

-- name: ConversationReadMarkers :many
SELECT conversation_id, marked_at AS read_at FROM conversation_marks
WHERE kind IN ('visit', 'read') AND user_id = ? ORDER BY conversation_id;

-- name: StartConversationReads :exec
INSERT INTO conversation_marks (kind, user_id, marked_at)
SELECT 'visit', sqlc.arg(user_id), CASE WHEN EXISTS (
    SELECT 1 FROM cache_sessions WHERE user_id = sqlc.arg(user_id) AND created_at < migrations.executed_at
) THEN unixepoch(migrations.executed_at) * 1000 ELSE (SELECT marked_at FROM next_conversation_mark) END
FROM migrations WHERE migration_name = '072-conversation-marks.sql'
ON CONFLICT (kind, user_id, conversation_id) DO NOTHING;
