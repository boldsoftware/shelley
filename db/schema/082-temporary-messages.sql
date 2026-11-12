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
