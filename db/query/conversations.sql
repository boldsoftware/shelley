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
    SELECT json_group_array(json_object(
             'email', participant.user_email,
             'message_count', participant.message_count))
      FROM (
        SELECT m.user_email, COUNT(*) AS message_count
          FROM messages m
         WHERE m.conversation_id = c.conversation_id
           AND m.user_email IS NOT NULL AND m.user_email <> ''
         GROUP BY m.user_email
      ) participant), '[]') AS TEXT) AS participants_json,
  -- Subagents ride idx_conversations_parent_id; running jobs ride
  -- idx_background_jobs_running.
  CAST((SELECT COUNT(*) FROM conversations sub
         WHERE sub.parent_conversation_id = c.conversation_id) AS INTEGER) AS subagent_count,
  CAST((SELECT COUNT(*) FROM background_jobs bj
         WHERE bj.conversation_id = c.conversation_id AND NOT bj.exited) AS INTEGER) AS running_background_jobs
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
    SELECT json_group_array(json_object(
             'email', participant.user_email,
             'message_count', participant.message_count))
      FROM (
        SELECT m.user_email, COUNT(*) AS message_count
          FROM messages m
         WHERE m.conversation_id = c.conversation_id
           AND m.user_email IS NOT NULL AND m.user_email <> ''
         GROUP BY m.user_email
      ) participant), '[]') AS TEXT) AS participants_json,
  -- Subagents ride idx_conversations_parent_id; running jobs ride
  -- idx_background_jobs_running.
  CAST((SELECT COUNT(*) FROM conversations sub
         WHERE sub.parent_conversation_id = c.conversation_id) AS INTEGER) AS subagent_count,
  CAST((SELECT COUNT(*) FROM background_jobs bj
         WHERE bj.conversation_id = c.conversation_id AND NOT bj.exited) AS INTEGER) AS running_background_jobs
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
    SELECT json_group_array(json_object(
             'email', participant.user_email,
             'message_count', participant.message_count))
      FROM (
        SELECT m.user_email, COUNT(*) AS message_count
          FROM messages m
         WHERE m.conversation_id = c.conversation_id
           AND m.user_email IS NOT NULL AND m.user_email <> ''
         GROUP BY m.user_email
      ) participant), '[]') AS TEXT) AS participants_json,
  -- Subagents ride idx_conversations_parent_id; running jobs ride
  -- idx_background_jobs_running.
  CAST((SELECT COUNT(*) FROM conversations sub
         WHERE sub.parent_conversation_id = c.conversation_id) AS INTEGER) AS subagent_count,
  CAST((SELECT COUNT(*) FROM background_jobs bj
         WHERE bj.conversation_id = c.conversation_id AND NOT bj.exited) AS INTEGER) AS running_background_jobs
FROM conversations c
WHERE c.slug LIKE '%' || ? || '%' AND c.archived = FALSE AND c.parent_conversation_id IS NULL
ORDER BY c.updated_at DESC
LIMIT ? OFFSET ?;

-- name: SearchConversationsWithMessages :many
-- Search conversations by slug OR message content (user messages and agent responses, not system prompts)
-- Includes both top-level conversations and subagent conversations
SELECT DISTINCT sqlc.embed(c),
  CAST(CASE WHEN c.slug LIKE '%' || sqlc.arg('query') || '%' THEN 0 ELSE 1 END AS INTEGER) AS slug_rank,
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
    SELECT json_group_array(json_object(
             'email', participant.user_email,
             'message_count', participant.message_count))
      FROM (
        SELECT pm.user_email, COUNT(*) AS message_count
          FROM messages pm
         WHERE pm.conversation_id = c.conversation_id
           AND pm.user_email IS NOT NULL AND pm.user_email <> ''
         GROUP BY pm.user_email
      ) participant), '[]') AS TEXT) AS participants_json,
  -- Subagents ride idx_conversations_parent_id; running jobs ride
  -- idx_background_jobs_running.
  CAST((SELECT COUNT(*) FROM conversations sub
         WHERE sub.parent_conversation_id = c.conversation_id) AS INTEGER) AS subagent_count,
  CAST((SELECT COUNT(*) FROM background_jobs bj
         WHERE bj.conversation_id = c.conversation_id AND NOT bj.exited) AS INTEGER) AS running_background_jobs
FROM conversations c
LEFT JOIN messages m ON c.conversation_id = m.conversation_id AND m.type IN ('user', 'agent')
WHERE c.archived = FALSE
  AND (
    c.slug LIKE '%' || sqlc.arg('query') || '%'
    OR json_extract(m.user_data, '$.text') LIKE '%' || sqlc.arg('query') || '%'
    OR m.llm_data LIKE '%' || sqlc.arg('query') || '%'
  )
ORDER BY slug_rank, c.updated_at DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: SearchConversationsFTSList :many
-- Top-level conversations matching either a slug substring or an FTS5 MATCH
-- against messages_fts, with slug matches first. The caller builds
-- both the LIKE pattern (with %, _, \ pre-escaped) and the MATCH
-- expression from user input.
WITH fts_hits AS (
  SELECT DISTINCT m.conversation_id
  FROM messages_fts
  CROSS JOIN messages m ON m.rowid = messages_fts.rowid
  CROSS JOIN conversations eligible ON eligible.conversation_id = m.conversation_id
  WHERE messages_fts MATCH @fts_match
    AND eligible.parent_conversation_id IS NULL
    AND eligible.archived = sqlc.arg('archived')
    AND json_extract(eligible.conversation_options, '$.kind') IS NOT 'search'
  ORDER BY eligible.updated_at DESC
  LIMIT sqlc.arg('limit') + sqlc.arg('offset')
)
SELECT sqlc.embed(c),
  CAST(CASE WHEN c.slug LIKE sqlc.arg('slug_like') ESCAPE '\' THEN 0 ELSE 1 END AS INTEGER) AS slug_rank,
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
    SELECT json_group_array(json_object(
             'email', participant.user_email,
             'message_count', participant.message_count))
      FROM (
        SELECT m.user_email, COUNT(*) AS message_count
          FROM messages m
         WHERE m.conversation_id = c.conversation_id
           AND m.user_email IS NOT NULL AND m.user_email <> ''
         GROUP BY m.user_email
      ) participant), '[]') AS TEXT) AS participants_json,
  -- Subagents ride idx_conversations_parent_id; running jobs ride
  -- idx_background_jobs_running.
  CAST((SELECT COUNT(*) FROM conversations sub
         WHERE sub.parent_conversation_id = c.conversation_id) AS INTEGER) AS subagent_count,
  CAST((SELECT COUNT(*) FROM background_jobs bj
         WHERE bj.conversation_id = c.conversation_id AND NOT bj.exited) AS INTEGER) AS running_background_jobs
FROM conversations c
WHERE c.parent_conversation_id IS NULL
  AND c.archived = sqlc.arg('archived')
  AND json_extract(c.conversation_options, '$.kind') IS NOT 'search'
  AND (
    c.slug LIKE @slug_like ESCAPE '\'
    OR c.conversation_id IN (SELECT conversation_id FROM fts_hits)
  )
ORDER BY slug_rank, c.archived ASC, c.updated_at DESC
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
SET current_generation = current_generation + 1,
    turn_interrupted = FALSE,
    updated_at = CURRENT_TIMESTAMP
WHERE conversation_id = ?
RETURNING *;

-- name: SetConversationGeneration :one
-- Used to roll back a failed compaction: the generation counter is bumped
-- before summarization runs, so on failure we restore the previous value to
-- keep the old (intact) generation active.
UPDATE conversations
SET current_generation = sqlc.arg('current_generation'),
    turn_interrupted = sqlc.arg('turn_interrupted'),
    updated_at = CURRENT_TIMESTAMP
WHERE conversation_id = sqlc.arg('conversation_id')
RETURNING *;

-- name: DeleteConversation :exec
DELETE FROM conversations
WHERE conversation_id = ?;

-- name: CountConversations :one
SELECT COUNT(*) FROM conversations WHERE archived = FALSE AND parent_conversation_id IS NULL;

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
-- Per-call LLM usage across all descendant conversations (subagents,
-- recursively). Powers the "plus $X for subagents" line in the token-cost
-- graph; the parent's own usage is not included. Returned one row per call
-- (not aggregated) so handleSubagentUsage can price each call by the
-- context-size tier its prompt lands in before summing.
WITH RECURSIVE descendants(conversation_id) AS (
  SELECT p.conversation_id FROM conversations p WHERE p.parent_conversation_id = ?
  UNION ALL
  SELECT c.conversation_id FROM conversations c
  JOIN descendants d ON c.parent_conversation_id = d.conversation_id
)
SELECT
  m.model_name,
  m.llm_api_url,
  CAST(COALESCE(m.usage_data ->> 'input_tokens', 0) AS INTEGER) AS input_tokens,
  CAST(COALESCE(m.usage_data ->> 'cache_creation_input_tokens', 0) AS INTEGER) AS cache_creation_input_tokens,
  CAST(COALESCE(m.usage_data ->> 'cache_read_input_tokens', 0) AS INTEGER) AS cache_read_input_tokens,
  CAST(COALESCE(m.usage_data ->> 'output_tokens', 0) AS INTEGER) AS output_tokens,
  CAST(COALESCE(m.usage_data ->> 'cost_usd', 0) AS REAL) AS cost_usd
-- CROSS JOIN keeps the small descendants set outermost. A regular JOIN lets
-- SQLite start from every agent message, which makes this query scan the full
-- messages table even when the conversation has no subagents.
FROM descendants d
CROSS JOIN messages m INDEXED BY idx_messages_conv_type_seq
WHERE m.conversation_id = d.conversation_id
  AND m.type = 'agent' AND m.usage_data IS NOT NULL
  -- Synthetic end-of-turn markers have usage_data but no tokens or cost.
  AND (m.usage_data ->> 'input_tokens' > 0
    OR m.usage_data ->> 'cache_creation_input_tokens' > 0
    OR m.usage_data ->> 'cache_read_input_tokens' > 0
    OR m.usage_data ->> 'output_tokens' > 0
    OR m.usage_data ->> 'cost_usd' > 0);

-- name: GetSubagentOtherUsage :many
-- Per-entry indirect LLM usage (messages.other_usage_data entries) across all
-- descendant conversations (subagents, recursively). Folded into
-- handleSubagentUsage's totals alongside GetSubagentUsage; the parent's own
-- indirect usage rides on its own messages instead. Returned one row per
-- entry (not aggregated) so each can be priced by its context-size tier.
WITH RECURSIVE descendants(conversation_id) AS (
  SELECT p.conversation_id FROM conversations p WHERE p.parent_conversation_id = ?
  UNION ALL
  SELECT c.conversation_id FROM conversations c
  JOIN descendants d ON c.parent_conversation_id = d.conversation_id
)
SELECT
  CAST(COALESCE(je.value ->> 'model', '') AS TEXT) AS model_name,
  CAST(COALESCE(je.value ->> 'url', '') AS TEXT) AS llm_api_url,
  CAST(COALESCE(je.value ->> 'input_tokens', 0) AS INTEGER) AS input_tokens,
  CAST(COALESCE(je.value ->> 'cache_creation_input_tokens', 0) AS INTEGER) AS cache_creation_input_tokens,
  CAST(COALESCE(je.value ->> 'cache_read_input_tokens', 0) AS INTEGER) AS cache_read_input_tokens,
  CAST(COALESCE(je.value ->> 'output_tokens', 0) AS INTEGER) AS output_tokens,
  CAST(COALESCE(je.value ->> 'cost_usd', 0) AS REAL) AS cost_usd
-- Keep descendants outermost here too, before expanding each matching
-- message's JSON. See GetSubagentUsage above.
FROM descendants d
CROSS JOIN messages m INDEXED BY idx_messages_conversation_id
CROSS JOIN json_each(m.other_usage_data) je
WHERE m.conversation_id = d.conversation_id
  AND m.other_usage_data IS NOT NULL;

-- name: GetConversationBySlugAndParent :one
SELECT * FROM conversations
WHERE slug = ? AND parent_conversation_id = ?;

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
-- Sets the agent_working flag. Starting any turn also resolves a prior
-- interruption: the user either clicked Continue or deliberately sent new
-- input instead. Deliberately does NOT bump updated_at; runtime state changes
-- must not reorder the conversation list.
UPDATE conversations
SET agent_working = sqlc.arg('agent_working'),
    turn_interrupted = CASE
      WHEN sqlc.arg('agent_working') THEN FALSE
      ELSE turn_interrupted
    END
WHERE conversation_id = sqlc.arg('conversation_id');

-- name: SetConversationTurnInterrupted :exec
-- Records durable user-facing restart state without adding transcript rows or
-- reordering the conversation list.
UPDATE conversations
SET turn_interrupted = sqlc.arg('turn_interrupted')
WHERE conversation_id = sqlc.arg('conversation_id');

-- name: ClaimInterruptedTurn :execrows
-- Atomically moves a manually resumed conversation from interrupted+idle to
-- working. The guard makes duplicate clicks and cross-request races harmless.
UPDATE conversations
SET turn_interrupted = FALSE,
    agent_working = TRUE
WHERE conversation_id = sqlc.arg('conversation_id')
  AND turn_interrupted = TRUE
  AND agent_working = FALSE
  AND parent_conversation_id IS NULL;

-- name: ClaimUpgradeInterruptedTurn :execrows
-- Atomically validates the durable turn version captured before listeners
-- opened and marks that exact stale turn claimed. The marker is hidden while
-- agent_working is true, survives another crash as a manual interruption, and
-- prevents duplicate resume workers. New user work changes the message version.
UPDATE conversations
SET turn_interrupted = TRUE
WHERE conversations.conversation_id = sqlc.arg('conversation_id')
  AND conversations.agent_working = TRUE
  AND conversations.turn_interrupted = FALSE
  AND conversations.current_generation = sqlc.arg('current_generation')
  AND (SELECT COALESCE(MAX(sequence_id), 0)
       FROM messages
       WHERE messages.conversation_id = conversations.conversation_id
         AND messages.type = 'user') = sqlc.arg('max_user_sequence_id');

-- name: FinishUpgradeInterruptedTurn :execrows
-- Clears the hidden claim immediately before the automatic retry. Failures
-- after this point restore the visible manual interruption state.
UPDATE conversations
SET turn_interrupted = FALSE
WHERE conversation_id = sqlc.arg('conversation_id')
  AND agent_working = TRUE
  AND turn_interrupted = TRUE;

-- name: MarkUpgradeResumeInterrupted :execrows
-- Converts a failed automatic resume into the ordinary manual-recovery state,
-- but only while the startup token still names the same durable turn.
-- Subagents have no manual resume; they just become idle.
UPDATE conversations
SET agent_working = FALSE,
    turn_interrupted = conversations.parent_conversation_id IS NULL
WHERE conversations.conversation_id = sqlc.arg('conversation_id')
  AND conversations.agent_working = TRUE
  AND conversations.current_generation = sqlc.arg('current_generation')
  AND (SELECT COALESCE(MAX(sequence_id), 0)
       FROM messages
       WHERE messages.conversation_id = conversations.conversation_id
         AND messages.type = 'user') = sqlc.arg('max_user_sequence_id');

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
-- Best-ranked snippet per conversation for the given conversation IDs.
-- snippet(table, columnIndex=-1 (any), start, end, ellipsis, tokenCount).
WITH ranked AS (
  SELECT m.conversation_id, m.message_id,
         CAST(m.rowid AS INTEGER) AS message_rowid,
         row_number() OVER (
           PARTITION BY m.conversation_id
           ORDER BY hits.rank
         ) AS rank_in_conversation
  FROM messages m
  JOIN messages_fts hits ON hits.rowid = m.rowid
  WHERE hits.messages_fts MATCH @fts_match
    AND m.conversation_id IN (sqlc.slice('conv_ids'))
)
SELECT ranked.conversation_id, ranked.message_id,
       snippet(messages_fts, 0, sqlc.arg(mark_start), sqlc.arg(mark_end), '...', 16) AS snippet
-- Keep FTS outermost so the prefix scan starts once, not once per snippet.
FROM messages_fts
CROSS JOIN ranked ON ranked.message_rowid = messages_fts.rowid
WHERE ranked.rank_in_conversation = 1
  AND messages_fts.messages_fts MATCH @fts_match;

-- name: UpdateConversationTags :one
-- Tagging is a metadata-only edit; deliberately does not bump updated_at
-- so retagging old conversations doesn't reorder the list.
UPDATE conversations
SET tags = ?
WHERE conversation_id = ?
RETURNING *;

-- name: ListConversationsWithQueuedTranscriptions :many
-- Every conversation (archived or not) whose durable queue holds a
-- transcription item. Used once at startup to recover detached workers.
SELECT * FROM conversations
WHERE queued_messages LIKE '%"kind":"transcription"%'
ORDER BY created_at ASC;

-- name: ListCommitTourWorkers :many
-- Specialized child conversations durably tracking requested commit tours.
SELECT * FROM conversations
WHERE parent_conversation_id IS NOT NULL
  AND user_initiated = FALSE
  AND conversation_options LIKE '%"kind":"commit-tour"%'
ORDER BY created_at ASC, rowid ASC;

-- name: CreateChannelConversation :one
INSERT INTO conversations (conversation_id, user_initiated, model, conversation_options, external_conversation_id, external_endpoint)
VALUES (?, TRUE, ?, ?, ?, ?)
RETURNING *;

-- name: GetConversationByExternalID :one
SELECT * FROM conversations WHERE external_conversation_id = ?;

-- name: ListChannelConversationsWithQueuedMessages :many
-- Conversations bound to an external chat with queued messages. Used once
-- at startup to drain them.
SELECT * FROM conversations
WHERE external_conversation_id IS NOT NULL AND queued_messages != '[]'
ORDER BY created_at ASC;
