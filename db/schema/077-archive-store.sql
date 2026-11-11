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
