-- Archive research (server/archive_research.go): the archive store's
-- research index and read API.

-- name: SearchArchiveResearch :many
-- One tier's best matches: agent replies, or (agent false) what people
-- wrote, titles and instructions. Ranked by bm25, weighted toward human
-- text and recent text; total is the tier's number of matches.
SELECT h.id, h.source_vm, h.conversation_id, h.project, h.sequence_id, h.kind, h.author_email, h.via, h.created_at,
  CAST(h.snippet AS TEXT) AS snippet,
  CAST(coalesce(c.slug, '') AS TEXT) AS slug,
  CAST(coalesce(c.archived, FALSE) AS BOOLEAN) AS archived,
  CAST(c.parent_conversation_id IS NOT NULL AS BOOLEAN) AS subagent,
  s.email AS source_email,
  CAST(coalesce((SELECT a.user_id FROM archive_store_sources a
    WHERE h.author_email <> '' AND lower(a.email) = lower(h.author_email) LIMIT 1), '') AS TEXT) AS author_user_id,
  CAST(COUNT(*) OVER () AS INTEGER) AS total
-- bm25 and snippet need the MATCH's own query, which can't be a window
-- query.
FROM (
  SELECT d.*, bm25(archive_research_fts) AS score,
    snippet(archive_research_fts, 0, sqlc.arg(mark_start), sqlc.arg(mark_end), '...', 24) AS snippet
  FROM archive_research_fts
  JOIN archive_research_docs d ON d.id = archive_research_fts.rowid
  WHERE archive_research_fts MATCH sqlc.arg(fts_match)
    AND (d.kind = 'agent') = CAST(sqlc.arg(agent) AS BOOLEAN)
    AND (CAST(sqlc.arg(author) AS TEXT) = '' OR instr(lower(d.author_email), lower(sqlc.arg(author))) > 0)
    AND (CAST(sqlc.arg(project) AS TEXT) = '' OR lower(d.project) = lower(sqlc.arg(project)))
    AND (CAST(sqlc.arg(source_vm) AS TEXT) = '' OR d.source_vm = sqlc.arg(source_vm))
    AND (CAST(sqlc.arg(conversation_id) AS TEXT) = '' OR d.conversation_id = sqlc.arg(conversation_id))
    AND (CAST(sqlc.arg(since) AS TEXT) = '' OR julianday(d.created_at) >= julianday(sqlc.arg(since)))
    AND (CAST(sqlc.arg(until) AS TEXT) = '' OR julianday(d.created_at) < julianday(sqlc.arg(until)))
) h
JOIN archive_store_sources s ON s.source_vm = h.source_vm
LEFT JOIN archive_store_conversations c ON c.source_vm = h.source_vm AND c.conversation_id = h.conversation_id
-- bm25 is negative, lower better: weights above 1 favor a kind.
ORDER BY h.score
  * (CASE h.kind WHEN 'human' THEN 1.0 WHEN 'instructions' THEN 0.8 WHEN 'title' THEN 0.6 WHEN 'unattributed' THEN 0.5 ELSE 1.0 END)
  / (1.0 + max(0, julianday('now') - julianday(h.created_at)) / 180.0)
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: GetArchiveResearchConversation :one
SELECT c.*, s.email AS source_email, s.user_id AS source_user_id
FROM archive_store_conversations c
JOIN archive_store_sources s USING (source_vm)
WHERE c.source_vm = ? AND c.conversation_id = ?;

-- name: ListArchiveResearchThread :many
-- A conversation's messages after a sequence id: who sent each, its
-- document if it is one, and its tool calls.
SELECT m.sequence_id, m.type, m.created_at,
  CAST(coalesce(d.author_email, m.user_email, '') AS TEXT) AS author_email,
  CAST(coalesce(d.kind, '') AS TEXT) AS kind,
  CAST(coalesce(d.text, '') AS TEXT) AS text,
  CAST(coalesce(d.via, '') AS TEXT) AS via,
  CAST((SELECT COUNT(*) FROM json_each(m.llm_data, '$.Content') x
        WHERE json_valid(m.llm_data) AND json_extract(x.value, '$.Type') = 5) AS INTEGER) AS tool_calls,
  CAST(EXISTS (SELECT 1 FROM json_each(m.llm_data, '$.Content') x
        WHERE json_valid(m.llm_data) AND json_extract(x.value, '$.Type') = 6) AS BOOLEAN) AS tool_result
FROM archive_store_messages m
LEFT JOIN archive_research_docs d ON d.message_id = m.id
WHERE m.source_vm = ? AND m.conversation_id = ? AND m.sequence_id > ?
ORDER BY m.sequence_id;

-- name: ListArchiveResearchConversations :many
-- Top-level conversations, most recently updated first, with who wrote in
-- them and how they open. author matches a person who wrote in one, or
-- its source's user.
SELECT c.source_vm, c.conversation_id, CAST(coalesce(c.slug, '') AS TEXT) AS slug, c.project,
  c.archived, c.created_at, c.updated_at, s.email AS source_email,
  CAST((SELECT COUNT(*) FROM archive_research_docs d
        WHERE d.source_vm = c.source_vm AND d.conversation_id = c.conversation_id AND d.kind = 'human') AS INTEGER) AS human_messages,
  CAST(coalesce((SELECT group_concat(DISTINCT d.author_email) FROM archive_research_docs d
        WHERE d.source_vm = c.source_vm AND d.conversation_id = c.conversation_id AND d.kind = 'human'), '') AS TEXT) AS authors,
  CAST(coalesce(o.kind, '') AS TEXT) AS opening_kind,
  CAST(coalesce(o.author_email, '') AS TEXT) AS opening_author,
  CAST(coalesce(substr(o.text, 1, 240), '') AS TEXT) AS opening,
  CAST(COUNT(*) OVER () AS INTEGER) AS total
FROM archive_store_conversations c
JOIN archive_store_sources s USING (source_vm)
LEFT JOIN archive_research_docs o ON o.id = (
  SELECT d.id FROM archive_research_docs d
  WHERE d.source_vm = c.source_vm AND d.conversation_id = c.conversation_id AND d.kind IN ('human', 'unattributed')
  ORDER BY d.sequence_id LIMIT 1)
WHERE c.parent_conversation_id IS NULL
  AND (CAST(sqlc.arg(project) AS TEXT) = '' OR lower(c.project) = lower(sqlc.arg(project)))
  AND (CAST(sqlc.arg(source_vm) AS TEXT) = '' OR c.source_vm = sqlc.arg(source_vm))
  AND (CAST(sqlc.arg(author) AS TEXT) = '' OR instr(lower(s.email), lower(sqlc.arg(author))) > 0 OR EXISTS (
    SELECT 1 FROM archive_research_docs d
    WHERE d.source_vm = c.source_vm AND d.conversation_id = c.conversation_id AND d.kind = 'human'
      AND instr(lower(d.author_email), lower(sqlc.arg(author))) > 0))
  AND (CAST(sqlc.arg(since) AS TEXT) = '' OR julianday(c.updated_at) >= julianday(sqlc.arg(since)))
  AND (CAST(sqlc.arg(until) AS TEXT) = '' OR julianday(c.created_at) < julianday(sqlc.arg(until)))
ORDER BY c.updated_at DESC
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: ListArchiveResearchSources :many
SELECT source_vm, user_id, email, projects FROM archive_store_sources ORDER BY email, source_vm;

-- name: CountArchiveResearchProjectConversations :many
SELECT source_vm, project, CAST(COUNT(*) AS INTEGER) AS conversations
FROM archive_store_conversations
WHERE parent_conversation_id IS NULL
GROUP BY source_vm, project;

-- name: InsertArchiveResearchRead :exec
INSERT INTO archive_research_reads (user_id, email, source_vm, path, query) VALUES (?, ?, ?, ?, ?);

-- name: ListArchiveResearchReads :many
SELECT * FROM archive_research_reads ORDER BY id;
