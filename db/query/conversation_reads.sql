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

-- name: CustomConversationReplyTimes :many
-- The latest 10 agent messages with visible text among the last 100 messages
-- of each conversation with a reply mark, the replies the sidebar counts after
-- the user's read cursor.
SELECT m.conversation_id, CAST(unixepoch(m.created_at) * 1000 AS INTEGER) AS replied_at
FROM conversation_marks AS reply JOIN messages AS m ON m.message_id IN (
    SELECT r.message_id FROM messages AS r
    WHERE r.conversation_id = reply.conversation_id AND r.type IN ('agent', 'error')
      AND r.sequence_id > (SELECT MAX(sequence_id) - 100 FROM messages WHERE conversation_id = reply.conversation_id)
      AND r.forked_from_message_id IS NULL
      -- A cheap scan first: parsing every tool call's JSON is slow.
      AND instr(r.llm_data, '"Type":2,"Text":"') AND json_valid(r.llm_data)
      AND EXISTS (
        SELECT 1 FROM json_each(r.llm_data, '$.Content') AS content
        WHERE content.type = 'object' AND json_extract(content.value, '$.Type') = 2
          AND trim(COALESCE(json_extract(content.value, '$.Text'), '')) != ''
      )
    ORDER BY r.sequence_id DESC LIMIT 10
)
WHERE reply.kind = 'reply' AND reply.user_id = ''
  AND reply.conversation_id IN (sqlc.slice('conversation_ids'))
ORDER BY m.conversation_id, m.sequence_id;
