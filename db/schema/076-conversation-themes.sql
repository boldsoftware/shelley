CREATE TABLE conversation_themes (
    conversation_id TEXT PRIMARY KEY REFERENCES conversations(conversation_id) ON DELETE CASCADE,
    chapters_json TEXT NOT NULL,
    through_message_id TEXT NOT NULL
);
