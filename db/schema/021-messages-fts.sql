-- Full-text search index over user and agent messages.
--
-- We extract just the human-readable text from the message JSON (Text and
-- Thinking fields, recursively, anywhere they appear in Content /
-- ToolResult / etc.) and index that. Indexing the raw JSON blobs would
-- otherwise pollute snippets with structural keys like `Type :6,
-- Text :` and bury matches inside base64 image data.
--
-- Stores the extracted text itself (not contentless) so snippet() can
-- return the matched fragment. The extracted text is a projection of the
-- message JSON; we don't try to keep it byte-for-byte canonical, just
-- searchable.
CREATE VIRTUAL TABLE messages_fts USING fts5(
    text,
    tokenize='porter unicode61'
);

-- Populate from existing user/agent messages.
INSERT INTO messages_fts(rowid, text)
SELECT m.rowid, (
    SELECT group_concat(t.value, ' ')
    FROM json_tree(coalesce(m.user_data, m.llm_data)) t
    WHERE t.key IN ('Text', 'Thinking')
      AND t.type = 'text'
      AND length(t.value) > 0
)
FROM messages m
WHERE m.type IN ('user', 'agent');

-- Keep the FTS index in sync with the messages table. Contentless FTS5
-- tables need an explicit 'delete' command keyed on rowid to remove rows;
-- updates are delete + reinsert.
CREATE TRIGGER messages_fts_ai AFTER INSERT ON messages
WHEN new.type IN ('user', 'agent') BEGIN
    INSERT INTO messages_fts(rowid, text) VALUES (
        new.rowid,
        (SELECT group_concat(t.value, ' ')
         FROM json_tree(coalesce(new.user_data, new.llm_data)) t
         WHERE t.key IN ('Text', 'Thinking')
           AND t.type = 'text'
           AND length(t.value) > 0)
    );
END;

CREATE TRIGGER messages_fts_ad AFTER DELETE ON messages
WHEN old.type IN ('user', 'agent') BEGIN
    DELETE FROM messages_fts WHERE rowid = old.rowid;
END;

CREATE TRIGGER messages_fts_au AFTER UPDATE ON messages
WHEN old.type IN ('user', 'agent') OR new.type IN ('user', 'agent') BEGIN
    DELETE FROM messages_fts WHERE rowid = old.rowid;
    INSERT INTO messages_fts(rowid, text)
    SELECT new.rowid, (
        SELECT group_concat(t.value, ' ')
        FROM json_tree(coalesce(new.user_data, new.llm_data)) t
        WHERE t.key IN ('Text', 'Thinking')
          AND t.type = 'text'
          AND length(t.value) > 0
    )
    WHERE new.type IN ('user', 'agent');
END;
