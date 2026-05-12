-- name: CreateMessage :one
INSERT INTO messages (message_id, conversation_id, sequence_id, generation, type, llm_data, user_data, usage_data, display_data, excluded_from_context)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetNextSequenceID :one
SELECT COALESCE(MAX(sequence_id), 0) + 1 
FROM messages 
WHERE conversation_id = ?;

-- name: GetMessage :one
SELECT * FROM messages
WHERE message_id = ?;

-- name: ListMessages :many
SELECT * FROM messages
WHERE conversation_id = ?
ORDER BY sequence_id ASC;

-- name: ListMessagesForContext :many
SELECT m.* FROM messages m
INNER JOIN conversations c ON m.conversation_id = c.conversation_id
WHERE m.conversation_id = ?
  AND m.excluded_from_context = FALSE
  AND m.generation = c.current_generation
ORDER BY m.sequence_id ASC;

-- name: ListMessagesPaginated :many
SELECT * FROM messages
WHERE conversation_id = ?
ORDER BY sequence_id ASC
LIMIT ? OFFSET ?;

-- name: ListMessagesByType :many
SELECT * FROM messages
WHERE conversation_id = ? AND type = ?
ORDER BY sequence_id ASC;

-- name: GetLatestMessage :one
SELECT * FROM messages
WHERE conversation_id = ?
ORDER BY sequence_id DESC
LIMIT 1;

-- name: DeleteMessage :exec
DELETE FROM messages
WHERE message_id = ?;

-- name: DeleteConversationMessages :exec
DELETE FROM messages
WHERE conversation_id = ?;

-- name: CountMessagesInConversation :one
SELECT COUNT(*) FROM messages
WHERE conversation_id = ?;

-- name: CountMessagesByType :one
SELECT COUNT(*) FROM messages
WHERE conversation_id = ? AND type = ?;

-- name: ListMessagesSince :many
SELECT * FROM messages
WHERE conversation_id = ? AND sequence_id > ?
ORDER BY sequence_id ASC;

-- name: UpdateMessageUserData :exec
UPDATE messages SET user_data = ? WHERE message_id = ?;

-- name: UpdateMessageExcludedFromContext :exec
UPDATE messages SET excluded_from_context = ? WHERE message_id = ?;

-- name: GetLatestAgentMessagesForConversations :many
-- Returns the 5 most recent agent messages per unarchived conversation
-- (parents and subagents). The caller scans these to find the most recent
-- one with a non-empty text block - a tail of tool-only messages doesn't
-- leave the conversation with an empty preview. Bounded to the 500 most
-- recently updated conversations so the patch-stream recompute stays
-- cheap; anything outside the window renders with empty preview fields.
WITH recent_convs AS (
  SELECT conversation_id
  FROM conversations
  WHERE archived = FALSE
  ORDER BY updated_at DESC
  LIMIT 500
),
ranked AS (
  SELECT m.message_id, m.conversation_id, m.sequence_id, m.type,
         m.llm_data, m.user_data, m.usage_data, m.created_at,
         m.display_data, m.excluded_from_context, m.generation,
         ROW_NUMBER() OVER (PARTITION BY m.conversation_id ORDER BY m.sequence_id DESC) AS rn
  FROM messages m
  INNER JOIN recent_convs c ON m.conversation_id = c.conversation_id
  WHERE m.type = 'agent'
)
SELECT message_id, conversation_id, sequence_id, type,
       llm_data, user_data, usage_data, created_at,
       display_data, excluded_from_context, generation
FROM ranked
WHERE rn <= 5
ORDER BY conversation_id, sequence_id DESC;

-- name: ListAgentMessagesSinceLastUser :many
-- Returns the agent messages produced during the most recent user turn,
-- ordered newest-first. "Most recent user turn" = all agent messages
-- whose sequence_id is greater than the sequence_id of the most recent
-- user message (or all agent messages if there is no user message yet,
-- e.g. orchestrator-spawned conversations). Used by the end-of-turn
-- notification builder to pick a useful body line.
SELECT m.message_id, m.conversation_id, m.sequence_id, m.type,
       m.llm_data, m.user_data, m.usage_data, m.created_at,
       m.display_data, m.excluded_from_context, m.generation
FROM messages m
WHERE m.conversation_id = ? AND m.type = 'agent'
  AND m.sequence_id > COALESCE(
    (SELECT MAX(u.sequence_id) FROM messages u
     WHERE u.conversation_id = ? AND u.type = 'user'),
    0)
ORDER BY m.sequence_id DESC;
