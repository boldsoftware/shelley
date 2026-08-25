-- name: CreateConversation :one
INSERT INTO conversations (conversation_id, slug, user_initiated, cwd, model, conversation_options)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: CreateDraftConversation :one
-- Creates a conversation in draft state with the given initial draft text.
-- Drafts have no messages; the chat handler clears is_draft / draft when
-- the user sends their first message via PromoteDraftConversation.
INSERT INTO conversations (conversation_id, slug, user_initiated, cwd, model, conversation_options, is_draft, draft)
VALUES (?, ?, TRUE, ?, ?, ?, TRUE, ?)
RETURNING *;

-- name: UpdateConversationDraft :one
-- Partially updates a draft conversation: any NULL argument keeps the
-- current value (text, model, and cwd each update independently), and
-- updated_at bumps so the conversation list reorders. The is_draft guard
-- makes this atomic: a draft promoted concurrently yields no rows
-- (ErrConversationNotDraft) rather than mutating an active conversation,
-- whose cwd is immutable and whose model only changes through the /model
-- loop switch.
UPDATE conversations
SET draft = COALESCE(sqlc.narg('draft'), draft),
    model = COALESCE(sqlc.narg('model'), model),
    cwd = COALESCE(sqlc.narg('cwd'), cwd),
    updated_at = CURRENT_TIMESTAMP
WHERE conversation_id = sqlc.arg('conversation_id') AND is_draft = TRUE
RETURNING *;

-- name: PromoteDraftConversation :one
-- Clears the draft state when the user sends the first message.
UPDATE conversations
SET is_draft = FALSE, draft = '', updated_at = CURRENT_TIMESTAMP
WHERE conversation_id = ? AND is_draft = TRUE
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
     WHERE m.conversation_id = c.conversation_id), 0) AS INTEGER) AS max_sequence_id,
  -- participants_json: the distinct exe.dev accounts that authored messages in
  -- this conversation (messages.user_email, stamped from the X-ExeDev-Email
  -- header), as a JSON array. The order is SQLite's business, so
  -- db.decodeParticipants sorts it: the conversation-list patch stream hashes
  -- the marshalled list, and an unstable order would emit spurious diffs.
  -- Rides idx_messages_participants (a partial covering index over authored
  -- messages), so this costs one index seek per listed conversation.
  CAST(COALESCE((
    SELECT json_group_array(DISTINCT m.user_email) FROM messages m
     WHERE m.conversation_id = c.conversation_id
       AND m.user_email IS NOT NULL AND m.user_email <> ''), '[]') AS TEXT) AS participants_json
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
     WHERE m.conversation_id = c.conversation_id), 0) AS INTEGER) AS max_sequence_id,
  -- See participants_json note on ListConversations.
  CAST(COALESCE((
    SELECT json_group_array(DISTINCT m.user_email) FROM messages m
     WHERE m.conversation_id = c.conversation_id
       AND m.user_email IS NOT NULL AND m.user_email <> ''), '[]') AS TEXT) AS participants_json
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
     WHERE m.conversation_id = c.conversation_id), 0) AS INTEGER) AS max_sequence_id,
  -- See participants_json note on ListConversations.
  CAST(COALESCE((
    SELECT json_group_array(DISTINCT m.user_email) FROM messages m
     WHERE m.conversation_id = c.conversation_id
       AND m.user_email IS NOT NULL AND m.user_email <> ''), '[]') AS TEXT) AS participants_json
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
     WHERE pm.conversation_id = c.conversation_id), 0) AS INTEGER) AS max_sequence_id,
  -- See participants_json note on ListConversations.
  CAST(COALESCE((
    SELECT json_group_array(DISTINCT pm.user_email) FROM messages pm
     WHERE pm.conversation_id = c.conversation_id
       AND pm.user_email IS NOT NULL AND pm.user_email <> ''), '[]') AS TEXT) AS participants_json
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
     WHERE m.conversation_id = c.conversation_id), 0) AS INTEGER) AS max_sequence_id,
  -- See participants_json note on ListConversations.
  CAST(COALESCE((
    SELECT json_group_array(DISTINCT m.user_email) FROM messages m
     WHERE m.conversation_id = c.conversation_id
       AND m.user_email IS NOT NULL AND m.user_email <> ''), '[]') AS TEXT) AS participants_json
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

-- name: SetConversationGeneration :one
-- Used to roll back a failed compaction: the generation counter is bumped
-- before summarization runs, so on failure we restore the previous value to
-- keep the old (intact) generation active.
UPDATE conversations
SET current_generation = ?, updated_at = CURRENT_TIMESTAMP
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

-- name: GetSubagentUsage :many
-- Aggregate LLM usage across all descendant conversations (subagents,
-- recursively), grouped by model. Powers the "plus $X for subagents" line
-- in the token-cost graph; the parent's own usage is not included.
WITH RECURSIVE descendants(conversation_id) AS (
  SELECT p.conversation_id FROM conversations p WHERE p.parent_conversation_id = ?
  UNION ALL
  SELECT c.conversation_id FROM conversations c
  JOIN descendants d ON c.parent_conversation_id = d.conversation_id
)
SELECT
  m.model_name,
  m.llm_api_url,
  COUNT(*) AS llm_calls,
  CAST(COALESCE(SUM(m.usage_data ->> 'input_tokens'), 0) AS INTEGER) AS input_tokens,
  CAST(COALESCE(SUM(m.usage_data ->> 'cache_creation_input_tokens'), 0) AS INTEGER) AS cache_creation_input_tokens,
  CAST(COALESCE(SUM(m.usage_data ->> 'cache_read_input_tokens'), 0) AS INTEGER) AS cache_read_input_tokens,
  CAST(COALESCE(SUM(m.usage_data ->> 'output_tokens'), 0) AS INTEGER) AS output_tokens,
  CAST(COALESCE(SUM(m.usage_data ->> 'cost_usd'), 0) AS REAL) AS cost_usd
FROM messages m
JOIN descendants d ON m.conversation_id = d.conversation_id
WHERE m.type = 'agent' AND m.usage_data IS NOT NULL
GROUP BY m.model_name, m.llm_api_url;

-- name: GetSubagentOtherUsage :many
-- Aggregate indirect LLM usage (messages.other_usage_data entries) across all
-- descendant conversations (subagents, recursively), grouped by model.
-- Folded into handleSubagentUsage's totals alongside GetSubagentUsage; the
-- parent's own indirect usage rides on its own messages instead.
WITH RECURSIVE descendants(conversation_id) AS (
  SELECT p.conversation_id FROM conversations p WHERE p.parent_conversation_id = ?
  UNION ALL
  SELECT c.conversation_id FROM conversations c
  JOIN descendants d ON c.parent_conversation_id = d.conversation_id
)
SELECT
  CAST(COALESCE(je.value ->> 'model', '') AS TEXT) AS model_name,
  CAST(COALESCE(je.value ->> 'url', '') AS TEXT) AS llm_api_url,
  COUNT(*) AS llm_calls,
  CAST(COALESCE(SUM(je.value ->> 'input_tokens'), 0) AS INTEGER) AS input_tokens,
  CAST(COALESCE(SUM(je.value ->> 'cache_creation_input_tokens'), 0) AS INTEGER) AS cache_creation_input_tokens,
  CAST(COALESCE(SUM(je.value ->> 'cache_read_input_tokens'), 0) AS INTEGER) AS cache_read_input_tokens,
  CAST(COALESCE(SUM(je.value ->> 'output_tokens'), 0) AS INTEGER) AS output_tokens,
  CAST(COALESCE(SUM(je.value ->> 'cost_usd'), 0) AS REAL) AS cost_usd
FROM messages m
JOIN descendants d ON m.conversation_id = d.conversation_id,
  json_each(m.other_usage_data) je
WHERE m.other_usage_data IS NOT NULL
-- Group by the JSON expressions, not the aliases: bare model_name/llm_api_url
-- would resolve to the messages table's own columns (NULL here).
GROUP BY je.value ->> 'model', je.value ->> 'url';

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

-- name: GetConversationQueuedMessages :one
SELECT queued_messages FROM conversations
WHERE conversation_id = ?;

-- name: UpdateConversationQueuedMessages :exec
-- Replaces the queued-messages JSON array and bumps updated_at so the
-- conversation re-sorts and a list-patch diff is emitted for the change.
UPDATE conversations
SET queued_messages = ?, updated_at = CURRENT_TIMESTAMP
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

-- name: ListAgentWorkingConversationIDs :many
-- Conversations left with agent_working = TRUE by the previous process. Used
-- on the resume-after-upgrade path (see DB.ConsumeResumeAfterUpgrade) to find
-- the turns that were interrupted by the upgrade restart.
SELECT conversation_id FROM conversations
WHERE agent_working = TRUE
ORDER BY updated_at DESC;

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
