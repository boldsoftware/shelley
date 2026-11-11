-- Archive research (server/archive_research.go): the archive store's
-- research index, which puts what people wrote first. Each document is one
-- piece of text with who wrote it, of one kind:
--   human         a message a person typed, signed in through exe.dev
--                 (author_email is the X-ExeDev-Email that sent it), or
--                 texted to the source's Assistant (external_message_id;
--                 author_email is the source's user, via 'text');
--   unattributed  a typed user message with no sign-in recorded: an agent
--                 or script posting through the local API, or a person
--                 on the source VM without the proxy: never claimed as a
--                 person's;
--   agent         an agent's final reply of a turn, and its message_user
--                 texts (machine-written);
--   title         a conversation's title (often machine-generated);
--   instructions  a project's instructions (author not recorded).
-- Tool calls, tool results, intermediate agent text, thinking, notices
-- (subagent and job reports, action results) and compaction copies are
-- not documents: they stay in archive_store_messages only.
-- Triggers keep the documents in step with the store's tables; the views
-- classify, for the triggers and for the backfill below alike.

CREATE TABLE archive_research_docs (
    id INTEGER PRIMARY KEY,
    source_vm TEXT NOT NULL,
    -- '' for project instructions.
    conversation_id TEXT NOT NULL,
    project TEXT NOT NULL,
    -- archive_store_messages.id; NULL for titles and instructions.
    message_id INTEGER UNIQUE,
    sequence_id INTEGER NOT NULL,
    kind TEXT NOT NULL,
    author_email TEXT NOT NULL,
    -- 'text' for a channel text; '' otherwise.
    via TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    text TEXT NOT NULL
);
CREATE INDEX archive_research_docs_conversation ON archive_research_docs (source_vm, conversation_id, sequence_id);

CREATE VIRTUAL TABLE archive_research_fts USING fts5(
    text,
    content='archive_research_docs',
    content_rowid='id',
    tokenize='porter unicode61'
);

CREATE TRIGGER archive_research_docs_ai AFTER INSERT ON archive_research_docs BEGIN
    INSERT INTO archive_research_fts (rowid, text) VALUES (new.id, new.text);
END;

CREATE TRIGGER archive_research_docs_ad AFTER DELETE ON archive_research_docs BEGIN
    INSERT INTO archive_research_fts (archive_research_fts, rowid, text) VALUES ('delete', old.id, old.text);
END;

-- Each stored message's document, if it is one.
CREATE VIEW archive_research_message_docs AS
SELECT source_vm, conversation_id, project, id AS message_id, sequence_id, kind,
    author_email, via, created_at, text
FROM (
    SELECT m.id, m.source_vm, m.conversation_id, c.project, m.sequence_id, m.created_at,
        CASE WHEN m.external_message_id IS NOT NULL THEN s.email ELSE coalesce(m.user_email, '') END AS author_email,
        CASE WHEN m.external_message_id IS NOT NULL THEN 'text' ELSE '' END AS via,
        CASE
            WHEN m.type = 'user' THEN CASE
                -- Tool results, and messages with machine metadata
                -- (subagent and job reports, action results, nudges,
                -- compaction copies), aren't typed.
                WHEN EXISTS (SELECT 1 FROM json_each(m.llm_data, '$.Content') x
                             WHERE json_extract(x.value, '$.Type') = 6) THEN NULL
                WHEN m.user_data IS NOT NULL AND m.user_data NOT IN ('', 'null') AND (
                    NOT json_valid(m.user_data) OR EXISTS (
                        SELECT 1 FROM json_each(m.user_data) u WHERE u.key <> 'live_delivery_id'))
                    THEN NULL
                -- Only the source's Assistant takes channel texts: its
                -- owner's (Slack channel conversations aren't archived).
                WHEN coalesce(m.user_email, '') <> '' OR m.external_message_id IS NOT NULL THEN 'human'
                -- A subagent's prompts come from its parent agent.
                WHEN c.parent_conversation_id IS NULL THEN 'unattributed'
            END
            WHEN m.type = 'agent' THEN 'agent'
        END AS kind,
        CASE
            WHEN m.type = 'user' THEN (
                SELECT group_concat(json_extract(x.value, '$.Text'), char(10))
                FROM json_each(m.llm_data, '$.Content') x
                WHERE json_extract(x.value, '$.Type') = 2)
            ELSE (
                SELECT group_concat(t, char(10)) FROM (
                    SELECT json_extract(x.value, '$.Text') AS t
                    FROM json_each(m.llm_data, '$.Content') x
                    WHERE json_extract(x.value, '$.Type') = 2
                      AND json_extract(m.llm_data, '$.EndOfTurn') = 1
                    UNION ALL
                    SELECT json_extract(x.value, '$.ToolInput.text')
                    FROM json_each(m.llm_data, '$.Content') x
                    WHERE json_extract(x.value, '$.Type') = 5
                      AND json_extract(x.value, '$.ToolName') = 'message_user'))
        END AS text
    FROM archive_store_messages m
    JOIN archive_store_conversations c USING (source_vm, conversation_id)
    JOIN archive_store_sources s ON s.source_vm = m.source_vm
    WHERE m.type IN ('user', 'agent') AND json_valid(m.llm_data)
)
WHERE kind IS NOT NULL AND length(trim(coalesce(text, ''))) > 0;

-- Each conversation's title.
CREATE VIEW archive_research_title_docs AS
SELECT source_vm, conversation_id, project, NULL AS message_id, 0 AS sequence_id,
    'title' AS kind, '' AS author_email, '' AS via, created_at, slug AS text
FROM archive_store_conversations
WHERE length(trim(coalesce(slug, ''))) > 0;

-- Each source's projects' instructions, headed by the project's name.
CREATE VIEW archive_research_instruction_docs AS
SELECT s.source_vm, '' AS conversation_id, json_extract(p.value, '$.name') AS project,
    NULL AS message_id, 0 AS sequence_id, 'instructions' AS kind, '' AS author_email, '' AS via,
    s.last_push_at AS created_at,
    json_extract(p.value, '$.name') || char(10) || json_extract(p.value, '$.instructions') AS text
FROM archive_store_sources s, json_each(s.projects) p
WHERE length(trim(coalesce(json_extract(p.value, '$.instructions'), ''))) > 0;

CREATE TRIGGER archive_research_message_ai AFTER INSERT ON archive_store_messages BEGIN
    INSERT INTO archive_research_docs (source_vm, conversation_id, project, message_id, sequence_id, kind, author_email, via, created_at, text)
    SELECT * FROM archive_research_message_docs WHERE message_id = new.id;
END;

CREATE TRIGGER archive_research_message_ad AFTER DELETE ON archive_store_messages BEGIN
    DELETE FROM archive_research_docs WHERE message_id = old.id;
END;

CREATE TRIGGER archive_research_conversation_ai AFTER INSERT ON archive_store_conversations BEGIN
    INSERT INTO archive_research_docs (source_vm, conversation_id, project, message_id, sequence_id, kind, author_email, via, created_at, text)
    SELECT * FROM archive_research_title_docs
    WHERE source_vm = new.source_vm AND conversation_id = new.conversation_id;
END;

-- A push upserts the conversation every time; only a new title or project
-- changes its documents.
CREATE TRIGGER archive_research_conversation_au AFTER UPDATE ON archive_store_conversations
WHEN old.slug IS NOT new.slug OR old.project <> new.project BEGIN
    UPDATE archive_research_docs SET project = new.project
    WHERE source_vm = new.source_vm AND conversation_id = new.conversation_id;
    DELETE FROM archive_research_docs
    WHERE source_vm = new.source_vm AND conversation_id = new.conversation_id AND kind = 'title';
    INSERT INTO archive_research_docs (source_vm, conversation_id, project, message_id, sequence_id, kind, author_email, via, created_at, text)
    SELECT * FROM archive_research_title_docs
    WHERE source_vm = new.source_vm AND conversation_id = new.conversation_id;
END;

CREATE TRIGGER archive_research_conversation_ad AFTER DELETE ON archive_store_conversations BEGIN
    DELETE FROM archive_research_docs
    WHERE source_vm = old.source_vm AND conversation_id = old.conversation_id;
END;

CREATE TRIGGER archive_research_projects_au AFTER UPDATE OF projects ON archive_store_sources BEGIN
    DELETE FROM archive_research_docs WHERE source_vm = new.source_vm AND kind = 'instructions';
    INSERT INTO archive_research_docs (source_vm, conversation_id, project, message_id, sequence_id, kind, author_email, via, created_at, text)
    SELECT * FROM archive_research_instruction_docs WHERE source_vm = new.source_vm;
END;

INSERT INTO archive_research_docs (source_vm, conversation_id, project, message_id, sequence_id, kind, author_email, via, created_at, text)
SELECT * FROM archive_research_message_docs
UNION ALL SELECT * FROM archive_research_title_docs
UNION ALL SELECT * FROM archive_research_instruction_docs;

-- Who read what of the store through the research API, for auditing.
CREATE TABLE archive_research_reads (
    id INTEGER PRIMARY KEY,
    at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    user_id TEXT NOT NULL,
    email TEXT NOT NULL,
    -- The reading VM, for a read through a VM-to-VM integration.
    source_vm TEXT NOT NULL,
    path TEXT NOT NULL,
    query TEXT NOT NULL
);
