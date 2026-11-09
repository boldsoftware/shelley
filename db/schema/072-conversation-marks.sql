-- The previous trial kept sequence receipts and first visits separately.
DROP TABLE IF EXISTS custom_conversation_reads;
DELETE FROM settings WHERE key GLOB 'conversation_reads_started:*';

CREATE TABLE conversation_marks (
    kind TEXT NOT NULL CHECK (kind IN ('reply', 'visit', 'read')),
    user_id TEXT NOT NULL DEFAULT '',
    conversation_id TEXT NOT NULL DEFAULT '',
    marked_at INTEGER NOT NULL,
    PRIMARY KEY (kind, user_id, conversation_id)
) WITHOUT ROWID;

CREATE INDEX conversation_marks_by_time ON conversation_marks(marked_at);

-- Order simultaneous writes without relying on a browser's clock.
CREATE VIEW next_conversation_mark AS
SELECT MAX(CAST(unixepoch('subsec') * 1000 AS INTEGER),
           COALESCE((SELECT MAX(marked_at) + 1 FROM conversation_marks), 0)) AS marked_at;

CREATE TRIGGER delete_conversation_marks AFTER DELETE ON conversations
BEGIN
    DELETE FROM conversation_marks WHERE conversation_id = OLD.conversation_id;
END;
