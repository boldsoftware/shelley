-- Workspace mode's pinned Assistant conversation.
CREATE TABLE workspace (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    assistant_id TEXT NOT NULL UNIQUE REFERENCES conversations(conversation_id) ON DELETE RESTRICT
);
