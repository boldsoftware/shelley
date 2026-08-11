-- Durable task checkpoints and reusable project lessons live in new tables so
-- customized Shelley upgrades never need to modify upstream conversation data.
CREATE TABLE task_journal (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) ON DELETE CASCADE,
    event_type TEXT NOT NULL,
    summary TEXT NOT NULL,
    state_json TEXT NOT NULL DEFAULT '{}',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_task_journal_conversation_created
    ON task_journal(conversation_id, created_at DESC);

CREATE TABLE memory_items (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    scope TEXT NOT NULL,
    project_path TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL,
    title TEXT NOT NULL,
    content TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 1.0,
    source_conversation_id TEXT REFERENCES conversations(conversation_id) ON DELETE SET NULL,
    supersedes_id TEXT REFERENCES memory_items(id) ON DELETE SET NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_memory_items_scope_project_updated
    ON memory_items(scope, project_path, updated_at DESC);

CREATE UNIQUE INDEX idx_memory_items_identity
    ON memory_items(scope, project_path, kind, title);

CREATE VIRTUAL TABLE memory_items_fts USING fts5(
    title,
    content,
    content='memory_items',
    content_rowid='rowid'
);

CREATE TRIGGER memory_items_ai AFTER INSERT ON memory_items BEGIN
    INSERT INTO memory_items_fts(rowid, title, content)
    VALUES (new.rowid, new.title, new.content);
END;

CREATE TRIGGER memory_items_ad AFTER DELETE ON memory_items BEGIN
    INSERT INTO memory_items_fts(memory_items_fts, rowid, title, content)
    VALUES ('delete', old.rowid, old.title, old.content);
END;

CREATE TRIGGER memory_items_au AFTER UPDATE ON memory_items BEGIN
    INSERT INTO memory_items_fts(memory_items_fts, rowid, title, content)
    VALUES ('delete', old.rowid, old.title, old.content);
    INSERT INTO memory_items_fts(rowid, title, content)
    VALUES (new.rowid, new.title, new.content);
END;

CREATE TABLE dream_runs (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) ON DELETE CASCADE,
    project_path TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL,
    memory_count INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
