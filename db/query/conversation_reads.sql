-- name: ConversationAttention :many
SELECT conversation_id, marked_at FROM conversation_marks
WHERE kind = 'reply' AND user_id = '' AND conversation_id IN (sqlc.slice('conversation_ids'));

-- name: ClearConversationAttention :exec
DELETE FROM conversation_marks
WHERE kind = 'reply' AND user_id = '' AND conversation_id = ?;

-- name: UpsertConversationRead :one
-- A receipt may reach the latest reply or the user's unread mark.
INSERT INTO conversation_marks (kind, user_id, conversation_id, marked_at)
SELECT 'read', sqlc.arg(user_id), sqlc.arg(conversation_id), CAST(sqlc.arg(read_at) AS INTEGER)
WHERE EXISTS (
    SELECT 1 FROM conversation_marks
    WHERE kind = 'reply' AND user_id = '' AND conversation_id = sqlc.arg(conversation_id)
      AND marked_at >= sqlc.arg(read_at)
    UNION ALL
    SELECT 1 FROM conversation_unread_marks
    WHERE user_id = sqlc.arg(user_id) AND conversation_id = sqlc.arg(conversation_id)
      AND marked_at >= sqlc.arg(read_at)
)
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

-- name: MarkConversationUnread :one
INSERT INTO conversation_unread_marks (user_id, conversation_id, marked_at)
SELECT sqlc.arg(user_id), c.conversation_id, (SELECT marked_at FROM next_conversation_mark)
FROM conversations AS c WHERE c.conversation_id = sqlc.arg(conversation_id)
ON CONFLICT (user_id, conversation_id) DO UPDATE SET marked_at = MAX(marked_at, excluded.marked_at)
RETURNING marked_at;

-- name: ConversationUnreadMarks :many
-- Marks no receipt has reached yet.
SELECT mark.conversation_id, mark.marked_at AS unread_at FROM conversation_unread_marks AS mark
WHERE mark.user_id = ? AND NOT EXISTS (
    SELECT 1 FROM conversation_marks AS receipt
    WHERE receipt.kind = 'read' AND receipt.user_id = mark.user_id
      AND receipt.conversation_id = mark.conversation_id AND receipt.marked_at >= mark.marked_at
)
ORDER BY mark.conversation_id;
