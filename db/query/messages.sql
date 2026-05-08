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
SELECT m.* FROM messages m
INNER JOIN (
  SELECT msg.conversation_id, MAX(msg.sequence_id) AS max_seq
  FROM messages msg
  INNER JOIN conversations c ON msg.conversation_id = c.conversation_id
  WHERE msg.type = 'agent' AND c.archived = FALSE AND c.parent_conversation_id IS NULL
  GROUP BY msg.conversation_id
  ORDER BY max_seq DESC
  LIMIT 50
) latest ON m.conversation_id = latest.conversation_id AND m.sequence_id = latest.max_seq;
