-- name: CreateMessage :one
-- created_at is normally left NULL to default to CURRENT_TIMESTAMP; see
-- db.CreateMessageParams.CreatedAt for who overrides it and why.
INSERT INTO messages (message_id, conversation_id, sequence_id, generation, type, llm_data, user_data, usage_data, display_data, excluded_from_context, llm_api_url, model_name, user_email, other_usage_data, external_message_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE(sqlc.narg('created_at'), CURRENT_TIMESTAMP))
RETURNING *;

-- name: GetNextSequenceID :one
SELECT COALESCE(MAX(sequence_id), 0) + 1 
FROM messages 
WHERE conversation_id = ?;

-- name: GetMaxSequenceIDsForAllConversations :many
SELECT conversation_id, CAST(COALESCE(MAX(sequence_id), 0) AS INTEGER) AS max_sequence_id
FROM messages
GROUP BY conversation_id;

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

-- name: GetGenerationAtOrBeforeSequence :one
-- Returns the generation of the last message at or before a sequence_id.
-- Used by fork to copy the generation that was active at the fork point,
-- which may be older than the conversation's current_generation.
--
-- Slug markers are excluded, matching CopyMessagesForFork. A marker is stamped
-- with whatever current_generation holds when the (racing, 15s-timeout) slug
-- goroutine lands, so a marker written after a compaction bumped the generation
-- would otherwise answer this question with the new generation and make the fork
-- copy the wrong one.
SELECT generation FROM messages
WHERE conversation_id = ? AND sequence_id <= ? AND type != 'slug'
ORDER BY sequence_id DESC LIMIT 1;

-- name: CopyMessagesForFork :exec
-- Copies the messages of a source conversation's given generation, up to and
-- including a cutoff sequence_id, into a destination conversation. The copies
-- are renumbered to generation 1 (the destination starts a fresh generation
-- history), get new message_ids, and preserve content, ordering, and original
-- timestamps. Used to fork a conversation.
INSERT INTO messages (message_id, conversation_id, sequence_id, generation, type, llm_data, user_data, usage_data, display_data, excluded_from_context, llm_api_url, model_name, user_email, forked_from_message_id, created_at, other_usage_data)
SELECT lower(hex(randomblob(16))), sqlc.arg('dest_conversation_id'), m.sequence_id, 1, m.type, m.llm_data, m.user_data, m.usage_data, m.display_data, m.excluded_from_context, m.llm_api_url, m.model_name, m.user_email, m.message_id, m.created_at, m.other_usage_data
FROM messages m
WHERE m.conversation_id = sqlc.arg('source_conversation_id')
  AND m.sequence_id <= sqlc.arg('cutoff_sequence_id')
  AND m.generation = sqlc.arg('source_generation')
  -- Skip slug markers: a fork derives its slug from the source's synchronously,
  -- with no LLM call, so copying the marker would re-report a cost the fork
  -- never incurred. Leaving a hole in the copied sequence is fine here: this
  -- already copies a single generation while preserving source sequence_ids, so
  -- forks legitimately have gaps and clients must already tolerate them.
  AND m.type != 'slug'
ORDER BY m.sequence_id ASC;

-- name: ListMessagesByType :many
SELECT * FROM messages
WHERE conversation_id = ? AND type = ?
ORDER BY sequence_id ASC;

-- name: GetLatestActionableMessage :one
-- The latest actionable row. Invisible slug/call-transcript bookkeeping can
-- arrive after a turn's error; it must not disable Retry/Continue affordances.
SELECT * FROM messages
WHERE conversation_id = ? AND type NOT IN ('slug', 'livetranscript')
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

-- name: CountConsecutiveMessagesByType :one
-- Counts the trailing run of messages of the given type, i.e. those after the
-- last message of any OTHER type. Used to cap consecutive retry warnings.
--
-- Invisible slug/call-transcript bookkeeping doesn't break a run. Otherwise
-- an asynchronous metadata append could reset the retry-warning storm cap.
SELECT COUNT(*) FROM messages m
WHERE m.conversation_id = sqlc.arg('conversation_id')
  AND m.generation = sqlc.arg('generation')
  AND m.type = sqlc.arg('type')
  AND m.sequence_id > COALESCE(
    (SELECT MAX(prev.sequence_id) FROM messages prev
     WHERE prev.conversation_id = sqlc.arg('conversation_id')
       AND prev.generation = sqlc.arg('generation')
       AND prev.type != sqlc.arg('type')
       AND prev.type NOT IN ('slug', 'livetranscript')),
    0);

-- name: ListMessagesTail :many
-- Returns the last N messages in ascending order. If fewer than N
-- exist, returns all of them.
--
-- N counts VISIBLE messages, but invisible slug/call-transcript bookkeeping
-- inside the resulting window is still returned. Two reasons to do it this way rather than just excluding them:
-- an invisible bookkeeping row must not eat the window (?tail=1 would hand back
-- nothing displayable), yet dropping markers from the middle would punch holes
-- in the sequence space, and clients that detect lost messages by sequence
-- contiguity (see the iOS store's firstGapBoundary) would read those holes as
-- missing data and re-fetch forever.
SELECT m.* FROM messages m
WHERE m.conversation_id = sqlc.arg('conversation_id')
  AND m.sequence_id >= COALESCE((
    SELECT MIN(v.sequence_id) FROM (
      SELECT vis.sequence_id FROM messages vis
      WHERE vis.conversation_id = sqlc.arg('conversation_id')
        AND vis.type NOT IN ('slug', 'livetranscript')
      ORDER BY vis.sequence_id DESC
      LIMIT sqlc.arg('limit')
    ) v
  ), 0)
ORDER BY m.sequence_id ASC;

-- name: ListMessagesBefore :many
-- A page of rows before a sequence id, newest first.
SELECT * FROM messages
WHERE conversation_id = ? AND sequence_id < ?
ORDER BY sequence_id DESC
LIMIT ?;

-- name: ListMessagesSince :many
-- The client cache-repair path (?last_sequence_id=N). Must NOT filter any type:
-- it is what heals a client whose view of the sequence space has a hole, so it
-- has to be able to deliver every row, markers included. Deliberately different
-- from ListMessagesTail (a display window, which counts visible rows); don't
-- "make these consistent".
SELECT * FROM messages
WHERE conversation_id = ? AND sequence_id > ?
ORDER BY sequence_id ASC;

-- name: UpdateMessageUserData :exec
-- Mutating message rows is forbidden in production paths (messages are an
-- immutable, append-only log keyed by sequence_id and cached in the browser).
-- This UPDATE exists ONLY for the FTS-trigger test (TestMessages*), which
-- verifies the messages_fts AFTER UPDATE trigger re-indexes user_data.
UPDATE messages SET user_data = ? WHERE message_id = ?;

-- name: ListAgentMessagesSinceLastUser :many
-- Returns the agent messages produced during the most recent user turn,
-- ordered newest-first. "Most recent user turn" = all agent messages
-- whose sequence_id is greater than the sequence_id of the most recent
-- user message (or all agent messages if there is no user message yet).
-- Used by the end-of-turn
-- notification builder to pick a useful body line.
SELECT m.message_id, m.conversation_id, m.sequence_id, m.type,
       m.llm_data, m.user_data, m.usage_data, m.created_at,
       m.display_data, m.excluded_from_context, m.generation,
       m.llm_api_url, m.model_name, m.forked_from_message_id, m.user_email,
       m.other_usage_data, m.external_message_id
FROM messages m
WHERE m.conversation_id = ? AND m.type = 'agent'
  AND m.sequence_id > COALESCE(
    (SELECT MAX(u.sequence_id) FROM messages u
     WHERE u.conversation_id = ? AND u.type = 'user'),
    0)
ORDER BY m.sequence_id DESC;

-- name: ListTypedUserMessages :many
-- Messages the user typed: user messages that carry neither a tool result nor
-- machine metadata, other than the stamps of a copy that a compaction carried
-- into a later generation. Originals come first, newest first, then copies: a
-- copy's original, when the conversation has it, is the message the UI shows;
-- a fork of a compacted generation has only the copies.
SELECT * FROM messages
WHERE conversation_id = ? AND type = 'user'
  AND (user_data IS NULL OR user_data IN ('', 'null') OR (
    json_valid(user_data) AND NOT EXISTS (
      SELECT 1 FROM json_each(messages.user_data) u
      WHERE u.key NOT IN ('compaction_carried', 'carried_from_sequence_id', 'live_delivery_id')
    )
  ))
  AND NOT EXISTS (
    SELECT 1 FROM json_each(messages.llm_data, '$.Content') c
    WHERE json_extract(c.value, '$.Type') = 6
  )
ORDER BY COALESCE(user_data LIKE '%compaction_carried%', 0), sequence_id DESC;

-- name: GetAgentMessageBefore :one
-- The newest agent message before sequence_id: for a tool-result message,
-- the message whose tool calls it answers.
SELECT * FROM messages
WHERE conversation_id = ? AND type = 'agent' AND sequence_id < ?
ORDER BY sequence_id DESC
LIMIT 1;

-- name: HasExternalMessage :one
-- Whether a channel message was already recorded in the conversation.
SELECT EXISTS (
  SELECT 1 FROM messages WHERE conversation_id = ? AND external_message_id = ?
);

-- name: GetGenerationMaxSequenceID :one
SELECT CAST(COALESCE(MAX(sequence_id), 0) AS INTEGER) FROM messages
WHERE conversation_id = ? AND generation = ?;

-- name: ListMessagesThrough :many
-- The context-visible messages of a generation, up to and including sequence_id.
SELECT * FROM messages
WHERE conversation_id = ? AND generation = ? AND sequence_id <= ? AND excluded_from_context = FALSE
ORDER BY sequence_id ASC;

-- name: ListConversationParticipants :many
-- The authenticated authors of the conversations' messages, with their
-- message counts.
SELECT conversation_id, CAST(user_email AS TEXT) AS user_email, COUNT(*) AS message_count
FROM messages
WHERE user_email IS NOT NULL AND user_email <> '' AND conversation_id IN (sqlc.slice('conversation_ids'))
GROUP BY conversation_id, user_email
ORDER BY conversation_id, user_email;

-- name: LatestUserExternalMessageID :one
-- The channel id of the conversation's latest user message (but for tool
-- results, type 6, and the copies a compaction carries forward), or NULL if it
-- did not come from a channel.
SELECT external_message_id FROM messages m
WHERE m.conversation_id = ? AND m.type = 'user'
  AND NOT (json_valid(m.user_data) AND json_type(m.user_data, '$.compaction_carried') IS NOT NULL)
  AND NOT EXISTS (SELECT 1 FROM json_each(m.llm_data, '$.Content') c WHERE json_extract(c.value, '$.Type') = 6)
ORDER BY m.sequence_id DESC LIMIT 1;

-- name: HasUserMessageAfter :one
-- Whether a user message other than a tool result (type 6) follows sequence_id.
SELECT CAST(EXISTS (
    SELECT 1 FROM messages m
    WHERE m.conversation_id = ? AND m.type = 'user' AND m.sequence_id > ?
      AND NOT EXISTS (SELECT 1 FROM json_each(m.llm_data, '$.Content') c WHERE json_extract(c.value, '$.Type') = 6)
) AS BOOLEAN);

-- name: SearchEnhancementModel :one
-- The model of the latest top-level, non-search conversation with a message by email.
SELECT CAST(c.model AS TEXT) FROM conversations c
WHERE c.parent_conversation_id IS NULL AND c.model IS NOT NULL AND c.model <> ''
  AND coalesce(json_extract(c.conversation_options, '$.kind'), '') <> 'search'
  AND EXISTS (
      SELECT 1 FROM messages m INDEXED BY idx_messages_participants
      WHERE m.conversation_id = c.conversation_id AND m.user_email = CAST(sqlc.arg(email) AS TEXT)
        AND m.user_email IS NOT NULL AND m.user_email <> ''
  )
ORDER BY c.updated_at DESC LIMIT 1;

-- name: SearchMessageContext :many
-- The text of the user and agent messages near each hit.
WITH hits AS (
    SELECT hm.message_id AS hit_id, hm.conversation_id AS hit_conversation_id, hm.sequence_id AS hit_sequence_id
    FROM messages hm WHERE hm.message_id IN (sqlc.slice('hit_ids'))
), nearby AS (
    SELECT h.hit_id, m.message_id, m.type, abs(m.sequence_id - h.hit_sequence_id) AS distance,
        substr((SELECT group_concat(j.value ->> 'Text', char(10))
            FROM json_each(CASE WHEN json_valid(m.llm_data) THEN m.llm_data ELSE '{}' END, '$.Content') j
            WHERE j.value ->> 'Type' = 2), 1, 6000) AS text
    FROM hits h CROSS JOIN messages m ON m.conversation_id = h.hit_conversation_id
        AND m.sequence_id BETWEEN h.hit_sequence_id - 12 AND h.hit_sequence_id + 6
    WHERE m.type IN ('user', 'agent')
      AND (m.type = 'agent' OR (
          json_extract(m.user_data, '$.sender_conversation_id') IS NULL
          AND json_extract(m.user_data, '$.background_job_id') IS NULL
          AND json_extract(m.user_data, '$.mcp_server_change') IS NULL
      ))
)
SELECT CAST(n.hit_id AS TEXT) AS hit_id, CAST(n.message_id AS TEXT) AS message_id, CAST(n.type AS TEXT) AS speaker,
    CAST(n.distance AS INTEGER) AS distance, CAST(n.text AS TEXT) AS text
FROM nearby n WHERE length(trim(n.text)) > 0;

-- name: ListConversationMessageSizes :many
-- Each conversation's message count and the bytes of its JSON payloads.
SELECT COUNT(*) AS messages,
    CAST(COALESCE(SUM(COALESCE(LENGTH(llm_data), 0) + COALESCE(LENGTH(user_data), 0) + COALESCE(LENGTH(display_data), 0)), 0) AS INTEGER) AS bytes
FROM messages
GROUP BY conversation_id;

-- name: CountMessagesByTypeAll :many
SELECT type, COUNT(*) AS count FROM messages GROUP BY type ORDER BY COUNT(*) DESC;

-- name: LatestExternalMessageWithPrefix :one
-- The latest message whose channel message id starts with prefix, such as a
-- Slack thread's.
SELECT message_id FROM messages
WHERE conversation_id = @conversation_id AND substr(external_message_id, 1, length(CAST(@prefix AS TEXT))) = CAST(@prefix AS TEXT)
ORDER BY sequence_id DESC LIMIT 1;
