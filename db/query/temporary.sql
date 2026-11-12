-- Temporary messages (db/schema/082-temporary-messages.sql).

-- name: OpenTemporaryRun :exec
INSERT OR IGNORE INTO temporary_runs (conversation_id) VALUES (?);

-- name: CloseTemporaryRun :exec
DELETE FROM temporary_runs WHERE conversation_id = ?;

-- name: ConversationTemporaryNow :one
-- Whether what the conversation does now is temporary: a run is open in it,
-- or it or an ancestor is a temporary conversation.
WITH RECURSIVE chain(id) AS (
  SELECT sqlc.arg('conversation_id')
  UNION ALL
  SELECT c.parent_conversation_id FROM conversations c JOIN chain ON c.conversation_id = chain.id
  WHERE c.parent_conversation_id IS NOT NULL
)
SELECT CAST(
  EXISTS (SELECT 1 FROM temporary_runs r WHERE r.conversation_id = sqlc.arg('conversation_id'))
  OR EXISTS (SELECT 1 FROM chain JOIN conversations c ON c.conversation_id = chain.id
    WHERE json_valid(c.conversation_options) AND json_extract(c.conversation_options, '$.temporary') = 1)
AS BOOLEAN) AS is_temporary;

-- name: MarkTemporarySubagent :execrows
-- Makes a new subagent a temporary conversation if its parent has a run
-- open.
UPDATE conversations
SET conversation_options = json_set(iif(json_valid(conversation_options), conversation_options, '{}'), '$.temporary', json('true'))
WHERE conversations.conversation_id = ?
  AND conversations.parent_conversation_id IN (SELECT conversation_id FROM temporary_runs);

-- name: ToolUseTemporary :one
-- Whether the message that made the tool call was temporary.
SELECT CAST(COUNT(*) > 0 AS BOOLEAN) AS is_temporary
FROM temporary_messages t
JOIN messages m ON m.message_id = t.message_id
WHERE t.conversation_id = sqlc.arg('conversation_id') AND m.type IN ('agent', 'tool')
  AND EXISTS (SELECT 1 FROM json_each(m.llm_data, '$.Content') x
    WHERE x.value ->> 'ID' = CAST(sqlc.arg('tool_use_id') AS TEXT));

-- name: ListTemporarySequenceIDs :many
SELECT sequence_id FROM temporary_messages WHERE conversation_id = ? ORDER BY sequence_id;
