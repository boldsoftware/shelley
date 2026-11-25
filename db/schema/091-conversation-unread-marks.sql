-- A user's "mark as unread". The conversation stays unread until a read
-- receipt reaches marked_at. Marks take next_conversation_mark, so a new
-- mark is later than every receipt.
CREATE TABLE conversation_unread_marks (
    user_id TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    marked_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, conversation_id)
) WITHOUT ROWID;

CREATE TRIGGER delete_conversation_unread_marks AFTER DELETE ON conversations
BEGIN
    DELETE FROM conversation_unread_marks WHERE conversation_id = OLD.conversation_id;
END;
