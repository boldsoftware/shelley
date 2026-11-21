-- The archive store's schema as migrations 077-082 left it, before
-- 085-remove-archive-store.sql (TestMigrateRemovesArchiveStoreKeepingData).

-- 077-archive-store.sql
-- Shelley archive store (server/archive_store.go): Shelleys' archivers push
-- their projects and conversations to it.

-- What sources pushed, keyed by their VM (the attested
-- X-Exedev-Source-Vm), attributed to the exe.dev user who pushed it.
CREATE TABLE archive_store_sources (
    source_vm TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    email TEXT NOT NULL,
    projects TEXT NOT NULL DEFAULT '[]',
    last_push_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE archive_store_conversations (
    source_vm TEXT NOT NULL REFERENCES archive_store_sources (source_vm) ON DELETE CASCADE,
    conversation_id TEXT NOT NULL,
    parent_conversation_id TEXT,
    slug TEXT,
    project TEXT NOT NULL DEFAULT '',
    model TEXT,
    cwd TEXT,
    archived BOOLEAN NOT NULL DEFAULT FALSE,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    -- The source's highest sequence id this store has covered: its messages
    -- up to here, less the ones the archiver doesn't push.
    have_seq INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (source_vm, conversation_id)
);
CREATE INDEX archive_store_conversations_updated ON archive_store_conversations (updated_at DESC);

CREATE TABLE archive_store_messages (
    id INTEGER PRIMARY KEY,
    source_vm TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    sequence_id INTEGER NOT NULL,
    message_id TEXT NOT NULL,
    type TEXT NOT NULL,
    llm_data TEXT,
    user_data TEXT,
    usage_data TEXT,
    display_data TEXT,
    created_at DATETIME NOT NULL,
    generation INTEGER NOT NULL,
    excluded_from_context BOOLEAN NOT NULL,
    model_name TEXT,
    user_email TEXT,
    UNIQUE (source_vm, conversation_id, sequence_id),
    FOREIGN KEY (source_vm, conversation_id)
        REFERENCES archive_store_conversations (source_vm, conversation_id) ON DELETE CASCADE
);

-- Search over what people and agents wrote, as messages_fts does, plus
-- message_user's text (the agent's replies in workspace mode).
CREATE VIRTUAL TABLE archive_store_messages_fts USING fts5(
    text,
    tokenize='porter unicode61'
);

CREATE TRIGGER archive_store_messages_fts_ai AFTER INSERT ON archive_store_messages
WHEN new.type IN ('user', 'agent') BEGIN
    INSERT INTO archive_store_messages_fts (rowid, text) VALUES (
        new.id,
        (SELECT group_concat(t.value, ' ')
         FROM json_tree(coalesce(new.user_data, new.llm_data)) t
         WHERE t.key IN ('Text', 'Thinking', 'text')
           AND t.type = 'text'
           AND length(t.value) > 0)
    );
END;

CREATE TRIGGER archive_store_messages_fts_ad AFTER DELETE ON archive_store_messages
WHEN old.type IN ('user', 'agent') BEGIN
    DELETE FROM archive_store_messages_fts WHERE rowid = old.id;
END;

-- 078-archiver.sql
-- The archiver (server/archiver.go): what this Shelley pushes to an archive
-- store, and what the store acked.

-- A conversation created while the archiver flag is on
-- (a non-empty string) is recorded here, with what the store has acked of it.
-- Only recorded conversations are ever pushed, so nothing from before push
-- was turned on is. No foreign key: the row of a deleted conversation is how
-- the deletion reaches the store.
CREATE TABLE archiver_pushes (
    conversation_id TEXT PRIMARY KEY,
    -- The top-level conversation, whose options decide (temporary, kind).
    root_id TEXT NOT NULL,
    -- The highest message sequence_id the store acked.
    pushed_seq INTEGER NOT NULL DEFAULT 0,
    -- The metadata JSON the store last acked; '' if it has none.
    pushed_meta TEXT NOT NULL DEFAULT ''
);

-- The projects JSON the store last acked.
CREATE TABLE archiver_projects (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    projects TEXT NOT NULL
);

-- In the creating transaction, so every creation path records, and a
-- subagent is recorded if its parent is. Temporary and helper (kind)
-- conversations aren't; the push checks again, since a draft's options are
-- set when it is sent. Imported conversations are unrecorded by the import.
CREATE TRIGGER archiver_record AFTER INSERT ON conversations
-- Options that aren't JSON ('' in old rows and tests) are no options.
WHEN json_extract(iif(json_valid(NEW.conversation_options), NEW.conversation_options, '{}'), '$.temporary') IS NOT 1
  AND json_extract(iif(json_valid(NEW.conversation_options), NEW.conversation_options, '{}'), '$.kind') IS NULL
  AND EXISTS (
    SELECT 1 FROM feature_flags
    WHERE name = 'archiver' AND json_valid(value)
      AND json_type(value) = 'text' AND value ->> '$' <> ''
  )
  AND (NEW.parent_conversation_id IS NULL OR EXISTS (
    SELECT 1 FROM archiver_pushes WHERE conversation_id = NEW.parent_conversation_id
  ))
BEGIN
    INSERT INTO archiver_pushes (conversation_id, root_id)
    VALUES (NEW.conversation_id, COALESCE(
        (SELECT root_id FROM archiver_pushes WHERE conversation_id = NEW.parent_conversation_id),
        NEW.conversation_id));
END;

-- 079-archive-store-external-message-id.sql
-- A message's channel id (messages.external_message_id), as archivers push
-- it: a text the source's owner sent, rather than typed in Shelley. NULL
-- for other messages, and from archivers that predate it.
ALTER TABLE archive_store_messages ADD COLUMN external_message_id TEXT;

-- 080-archive-store-times.sql
-- The archive store kept pushed times in Go's text form ("2006-01-02
-- 15:04:05.999999999 +0000 UTC"), which SQLite's date functions don't
-- read. It now writes SQLite's ("2006-01-02 15:04:05", UTC, like Shelley's
-- own tables); this converts what it has. Pushed times are always UTC.
UPDATE archive_store_conversations SET created_at = substr(created_at, 1, 19)
WHERE created_at LIKE '% +0000 UTC';
UPDATE archive_store_conversations SET updated_at = substr(updated_at, 1, 19)
WHERE updated_at LIKE '% +0000 UTC';
UPDATE archive_store_messages SET created_at = substr(created_at, 1, 19)
WHERE created_at LIKE '% +0000 UTC';

-- 081-archive-research.sql
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

-- 082-temporary-messages.sql
-- Temporary messages: kept out of the archive store (server/archiver.go),
-- like temporary conversations, but one turn at a time (server/temporary.go).

-- A conversation with a temporary run open: every message recorded in it
-- is temporary. A message of temporary origin opens the run; the next
-- message the user types that starts a turn, not temporary, closes it.
CREATE TABLE IF NOT EXISTS temporary_runs (
    conversation_id TEXT PRIMARY KEY REFERENCES conversations(conversation_id) ON DELETE CASCADE
);

-- The temporary messages.
CREATE TABLE IF NOT EXISTS temporary_messages (
    message_id TEXT PRIMARY KEY REFERENCES messages(message_id) ON DELETE CASCADE,
    conversation_id TEXT NOT NULL,
    sequence_id INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS temporary_messages_by_sequence ON temporary_messages(conversation_id, sequence_id);

-- In the inserting transaction, so every path that records a message
-- classifies it before anything reads it. Besides a message of an open run,
-- a fork's copy of a temporary message is temporary, and so is an in-place
-- compaction whose summaries cover one.
CREATE TRIGGER IF NOT EXISTS temporary_message AFTER INSERT ON messages
WHEN EXISTS (SELECT 1 FROM temporary_runs WHERE conversation_id = NEW.conversation_id)
  OR EXISTS (SELECT 1 FROM temporary_messages WHERE message_id = NEW.forked_from_message_id)
  OR (NEW.type = 'inplacecompaction' AND json_valid(NEW.user_data) AND EXISTS (
    SELECT 1 FROM json_each(NEW.user_data, '$.squishes') s
    JOIN temporary_messages t ON t.conversation_id = NEW.conversation_id
      AND t.sequence_id BETWEEN s.value ->> '$.from_sequence_id' AND s.value ->> '$.to_sequence_id'
  ))
BEGIN
    INSERT INTO temporary_messages (message_id, conversation_id, sequence_id)
    VALUES (NEW.message_id, NEW.conversation_id, NEW.sequence_id);
END;
