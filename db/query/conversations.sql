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
SELECT sqlc.embed(c),
  -- preview_packed: locate the newest agent message that actually contains a
  -- text block (the EXISTS short-circuits on the first one), then pull that
  -- block. The outer ORDER BY rides idx_messages_conv_type_seq, so we stop at
  -- the first qualifying message instead of expanding and globally sorting
  -- every agent message's content blocks. The first 20 bytes are the fixed
  -- RFC3339 timestamp (strftime '%Y-%m-%dT%H:%M:%SZ'); the rest is the preview
  -- text capped to 300 chars so we don't haul multi-KB replies across the
  -- driver + JSON + gzip for a one-line UI field. db.splitPreviewPacked splits
  -- it back apart.
  CAST(COALESCE((
    SELECT strftime('%Y-%m-%dT%H:%M:%SZ', m.created_at) || substr((
             SELECT je.value ->> 'Text'
               FROM json_each(m.llm_data, '$.Content') je
              WHERE je.value ->> 'Type' = 2 AND je.value ->> 'Text' <> ''
              ORDER BY je.key DESC LIMIT 1), 1, 300)
      FROM messages m
     WHERE m.conversation_id = c.conversation_id AND m.type = 'agent'
       AND EXISTS (SELECT 1 FROM json_each(m.llm_data, '$.Content') je
                   WHERE je.value ->> 'Type' = 2 AND je.value ->> 'Text' <> '')
     ORDER BY m.sequence_id DESC LIMIT 1), '') AS TEXT) AS preview_packed,
  CAST(COALESCE((
    SELECT MAX(m.sequence_id) FROM messages m
     WHERE m.conversation_id = c.conversation_id), 0) AS INTEGER) AS max_sequence_id
FROM conversations c
WHERE c.archived = FALSE AND c.parent_conversation_id IS NULL
ORDER BY c.updated_at DESC
LIMIT ? OFFSET ?;

-- name: ListAllConversations :many
-- Like ListConversations but includes subagents. Used by the conversation
-- list patch stream so the UI can render subagents inline and pick up their
-- working state from diffs alone.
SELECT sqlc.embed(c),
  -- preview_packed: locate the newest agent message that actually contains a
  -- text block (the EXISTS short-circuits on the first one), then pull that
  -- block. The outer ORDER BY rides idx_messages_conv_type_seq, so we stop at
  -- the first qualifying message instead of expanding and globally sorting
  -- every agent message's content blocks. The first 20 bytes are the fixed
  -- RFC3339 timestamp (strftime '%Y-%m-%dT%H:%M:%SZ'); the rest is the preview
  -- text capped to 300 chars so we don't haul multi-KB replies across the
  -- driver + JSON + gzip for a one-line UI field. db.splitPreviewPacked splits
  -- it back apart.
  CAST(COALESCE((
    SELECT strftime('%Y-%m-%dT%H:%M:%SZ', m.created_at) || substr((
             SELECT je.value ->> 'Text'
               FROM json_each(m.llm_data, '$.Content') je
              WHERE je.value ->> 'Type' = 2 AND je.value ->> 'Text' <> ''
              ORDER BY je.key DESC LIMIT 1), 1, 300)
      FROM messages m
     WHERE m.conversation_id = c.conversation_id AND m.type = 'agent'
       AND EXISTS (SELECT 1 FROM json_each(m.llm_data, '$.Content') je
                   WHERE je.value ->> 'Type' = 2 AND je.value ->> 'Text' <> '')
     ORDER BY m.sequence_id DESC LIMIT 1), '') AS TEXT) AS preview_packed,
  CAST(COALESCE((
    SELECT MAX(m.sequence_id) FROM messages m
     WHERE m.conversation_id = c.conversation_id), 0) AS INTEGER) AS max_sequence_id
FROM conversations c
WHERE c.archived = FALSE
ORDER BY c.updated_at DESC
LIMIT ? OFFSET ?;

-- name: ListArchivedConversations :many
SELECT * FROM conversations
WHERE archived = TRUE
ORDER BY updated_at DESC
LIMIT ? OFFSET ?;

-- name: SearchConversations :many
SELECT sqlc.embed(c),
  -- preview_packed: locate the newest agent message that actually contains a
  -- text block (the EXISTS short-circuits on the first one), then pull that
  -- block. The outer ORDER BY rides idx_messages_conv_type_seq, so we stop at
  -- the first qualifying message instead of expanding and globally sorting
  -- every agent message's content blocks. The first 20 bytes are the fixed
  -- RFC3339 timestamp (strftime '%Y-%m-%dT%H:%M:%SZ'); the rest is the preview
  -- text capped to 300 chars so we don't haul multi-KB replies across the
  -- driver + JSON + gzip for a one-line UI field. db.splitPreviewPacked splits
  -- it back apart.
  CAST(COALESCE((
    SELECT strftime('%Y-%m-%dT%H:%M:%SZ', m.created_at) || substr((
             SELECT je.value ->> 'Text'
               FROM json_each(m.llm_data, '$.Content') je
              WHERE je.value ->> 'Type' = 2 AND je.value ->> 'Text' <> ''
              ORDER BY je.key DESC LIMIT 1), 1, 300)
      FROM messages m
     WHERE m.conversation_id = c.conversation_id AND m.type = 'agent'
       AND EXISTS (SELECT 1 FROM json_each(m.llm_data, '$.Content') je
                   WHERE je.value ->> 'Type' = 2 AND je.value ->> 'Text' <> '')
     ORDER BY m.sequence_id DESC LIMIT 1), '') AS TEXT) AS preview_packed,
  CAST(COALESCE((
    SELECT MAX(m.sequence_id) FROM messages m
     WHERE m.conversation_id = c.conversation_id), 0) AS INTEGER) AS max_sequence_id
FROM conversations c
WHERE c.slug LIKE '%' || ? || '%' AND c.archived = FALSE AND c.parent_conversation_id IS NULL
ORDER BY c.updated_at DESC
LIMIT ? OFFSET ?;

-- name: SearchConversationsWithMessages :many
-- Search conversations by slug OR message content (user messages and agent responses, not system prompts)
-- Includes both top-level conversations and subagent conversations
SELECT DISTINCT sqlc.embed(c),
  -- See preview_packed note on ListConversations. Inner messages alias is
  -- pm here to avoid colliding with the outer LEFT JOIN messages m.
  CAST(COALESCE((
    SELECT strftime('%Y-%m-%dT%H:%M:%SZ', pm.created_at) || substr((
             SELECT je.value ->> 'Text'
               FROM json_each(pm.llm_data, '$.Content') je
              WHERE je.value ->> 'Type' = 2 AND je.value ->> 'Text' <> ''
              ORDER BY je.key DESC LIMIT 1), 1, 300)
      FROM messages pm
     WHERE pm.conversation_id = c.conversation_id AND pm.type = 'agent'
       AND EXISTS (SELECT 1 FROM json_each(pm.llm_data, '$.Content') je
                   WHERE je.value ->> 'Type' = 2 AND je.value ->> 'Text' <> '')
     ORDER BY pm.sequence_id DESC LIMIT 1), '') AS TEXT) AS preview_packed,
  CAST(COALESCE((
    SELECT MAX(pm.sequence_id) FROM messages pm
     WHERE pm.conversation_id = c.conversation_id), 0) AS INTEGER) AS max_sequence_id
FROM conversations c
LEFT JOIN messages m ON c.conversation_id = m.conversation_id AND m.type IN ('user', 'agent')
WHERE c.archived = FALSE
  AND (
    c.slug LIKE '%' || ? || '%'
    OR json_extract(m.user_data, '$.text') LIKE '%' || ? || '%'
    OR m.llm_data LIKE '%' || ? || '%'
  )
ORDER BY c.updated_at DESC
LIMIT ? OFFSET ?;

-- name: SearchConversationsFTSList :many
-- Top-level conversations (active first, then archived) matching either a
-- slug substring or an FTS5 MATCH against messages_fts. The caller builds
-- both the LIKE pattern (with %, _, \ pre-escaped) and the MATCH
-- expression from user input.
WITH fts_hits AS (
  SELECT DISTINCT m.conversation_id
  FROM messages m
  JOIN messages_fts ON messages_fts.rowid = m.rowid
  WHERE messages_fts MATCH @fts_match
)
SELECT sqlc.embed(c),
  -- preview_packed: locate the newest agent message that actually contains a
  -- text block (the EXISTS short-circuits on the first one), then pull that
  -- block. The outer ORDER BY rides idx_messages_conv_type_seq, so we stop at
  -- the first qualifying message instead of expanding and globally sorting
  -- every agent message's content blocks. The first 20 bytes are the fixed
  -- RFC3339 timestamp (strftime '%Y-%m-%dT%H:%M:%SZ'); the rest is the preview
  -- text capped to 300 chars so we don't haul multi-KB replies across the
  -- driver + JSON + gzip for a one-line UI field. db.splitPreviewPacked splits
  -- it back apart.
  CAST(COALESCE((
    SELECT strftime('%Y-%m-%dT%H:%M:%SZ', m.created_at) || substr((
             SELECT je.value ->> 'Text'
               FROM json_each(m.llm_data, '$.Content') je
              WHERE je.value ->> 'Type' = 2 AND je.value ->> 'Text' <> ''
              ORDER BY je.key DESC LIMIT 1), 1, 300)
      FROM messages m
     WHERE m.conversation_id = c.conversation_id AND m.type = 'agent'
       AND EXISTS (SELECT 1 FROM json_each(m.llm_data, '$.Content') je
                   WHERE je.value ->> 'Type' = 2 AND je.value ->> 'Text' <> '')
     ORDER BY m.sequence_id DESC LIMIT 1), '') AS TEXT) AS preview_packed,
  CAST(COALESCE((
    SELECT MAX(m.sequence_id) FROM messages m
     WHERE m.conversation_id = c.conversation_id), 0) AS INTEGER) AS max_sequence_id
FROM conversations c
WHERE c.parent_conversation_id IS NULL
  AND (
    c.slug LIKE @slug_like ESCAPE '\'
    OR c.conversation_id IN (SELECT conversation_id FROM fts_hits)
  )
ORDER BY c.archived ASC, c.updated_at DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

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

-- name: SearchConversationsFTSSnippets :many
-- Best snippet per message for the given conversation IDs, ordered by
-- FTS rank so the caller can keep the first row seen per conversation.
-- snippet(table, columnIndex=-1 (any), start, end, ellipsis, tokenCount).
SELECT m.conversation_id,
       snippet(messages_fts, 0, sqlc.arg(mark_start), sqlc.arg(mark_end), '...', 16) AS snippet
FROM messages m
JOIN messages_fts ON messages_fts.rowid = m.rowid
WHERE messages_fts MATCH @fts_match
  AND m.conversation_id IN (sqlc.slice('conv_ids'))
ORDER BY messages_fts.rank;

-- name: UpdateConversationTags :one
-- Tagging is a metadata-only edit; deliberately does not bump updated_at
-- so retagging old conversations doesn't reorder the list.
UPDATE conversations
SET tags = ?
WHERE conversation_id = ?
RETURNING *;
