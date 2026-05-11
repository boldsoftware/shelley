-- name: CreateConversation :one
INSERT INTO conversations (conversation_id, slug, user_initiated, cwd, model, conversation_options)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetConversation :one
SELECT * FROM conversations
WHERE conversation_id = ?;

-- name: GetConversationBySlug :one
SELECT * FROM conversations
WHERE slug = ?;

-- name: ListConversations :many
SELECT * FROM conversations
WHERE archived = FALSE AND parent_conversation_id IS NULL
ORDER BY updated_at DESC
LIMIT ? OFFSET ?;

-- name: ListAllConversations :many
-- Like ListConversations but includes subagents. Used by the conversation
-- list patch stream so the UI can render subagents inline and pick up their
-- working state from diffs alone.
SELECT * FROM conversations
WHERE archived = FALSE
ORDER BY updated_at DESC
LIMIT ? OFFSET ?;

-- name: ListArchivedConversations :many
SELECT * FROM conversations
WHERE archived = TRUE
ORDER BY updated_at DESC
LIMIT ? OFFSET ?;

-- name: SearchConversations :many
SELECT * FROM conversations
WHERE slug LIKE '%' || ? || '%' AND archived = FALSE AND parent_conversation_id IS NULL
ORDER BY updated_at DESC
LIMIT ? OFFSET ?;

-- name: SearchConversationsWithMessages :many
-- Search conversations by slug OR message content (user messages and agent responses, not system prompts)
-- Includes both top-level conversations and subagent conversations
SELECT DISTINCT c.* FROM conversations c
LEFT JOIN messages m ON c.conversation_id = m.conversation_id AND m.type IN ('user', 'agent')
WHERE c.archived = FALSE
  AND (
    c.slug LIKE '%' || ? || '%'
    OR json_extract(m.user_data, '$.text') LIKE '%' || ? || '%'
    OR m.llm_data LIKE '%' || ? || '%'
  )
ORDER BY c.updated_at DESC
LIMIT ? OFFSET ?;

-- name: SearchArchivedConversations :many
SELECT * FROM conversations
WHERE slug LIKE '%' || ? || '%' AND archived = TRUE
ORDER BY updated_at DESC
LIMIT ? OFFSET ?;

-- name: UpdateConversationSlug :one
UPDATE conversations
SET slug = ?, updated_at = CURRENT_TIMESTAMP
WHERE conversation_id = ?
RETURNING *;

-- name: UpdateConversationTimestamp :exec
UPDATE conversations
SET updated_at = CURRENT_TIMESTAMP
WHERE conversation_id = ?;

-- name: IncrementConversationGeneration :one
UPDATE conversations
SET current_generation = current_generation + 1, updated_at = CURRENT_TIMESTAMP
WHERE conversation_id = ?
RETURNING *;

-- name: DeleteConversation :exec
DELETE FROM conversations
WHERE conversation_id = ?;

-- name: CountConversations :one
SELECT COUNT(*) FROM conversations WHERE archived = FALSE AND parent_conversation_id IS NULL;

-- name: CountArchivedConversations :one
SELECT COUNT(*) FROM conversations WHERE archived = TRUE;

-- name: ArchiveConversation :one
UPDATE conversations
SET archived = TRUE
WHERE conversation_id = ?
RETURNING *;

-- name: UnarchiveConversation :one
UPDATE conversations
SET archived = FALSE
WHERE conversation_id = ?
RETURNING *;

-- name: UpdateConversationCwd :one
UPDATE conversations
SET cwd = ?, updated_at = CURRENT_TIMESTAMP
WHERE conversation_id = ?
RETURNING *;


-- name: CreateSubagentConversation :one
INSERT INTO conversations (conversation_id, slug, user_initiated, cwd, parent_conversation_id)
VALUES (?, ?, FALSE, ?, ?)
RETURNING *;

-- name: GetSubagents :many
SELECT * FROM conversations
WHERE parent_conversation_id = ?
ORDER BY created_at ASC;

-- name: GetConversationBySlugAndParent :one
SELECT * FROM conversations
WHERE slug = ? AND parent_conversation_id = ?;

-- name: GetSubagentCounts :many
SELECT parent_conversation_id, COUNT(*) AS count
FROM conversations
WHERE parent_conversation_id IS NOT NULL
GROUP BY parent_conversation_id;

-- name: UpdateConversationModel :exec
UPDATE conversations
SET model = ?
WHERE conversation_id = ? AND model IS NULL;

-- name: ForceUpdateConversationModel :exec
UPDATE conversations
SET model = ?, updated_at = CURRENT_TIMESTAMP
WHERE conversation_id = ?;

-- name: GetConversationOptions :one
SELECT conversation_options FROM conversations
WHERE conversation_id = ?;

-- name: UpdateConversationOptions :exec
UPDATE conversations
SET conversation_options = ?
WHERE conversation_id = ?;

-- name: UpdateConversationParent :one
UPDATE conversations
SET parent_conversation_id = ?, updated_at = CURRENT_TIMESTAMP
WHERE conversation_id = ?
RETURNING *;

-- name: SetConversationAgentWorking :exec
-- Sets the agent_working flag. Deliberately does NOT bump updated_at:
-- working transitions happen at every loop start/finish and we don't want
-- them to reorder the conversation list. The patch stream picks the change
-- up via the standard Pool.OnCommit hook.
UPDATE conversations
SET agent_working = ?
WHERE conversation_id = ?;

-- name: ResetAllAgentWorking :exec
-- Called on server startup to clear any stale TRUE values left over from a
-- previous process that exited mid-turn. Does not bump updated_at.
UPDATE conversations
SET agent_working = FALSE
WHERE agent_working = TRUE;
