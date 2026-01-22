-- Add 'startup-hook' to the message type check constraint
-- This requires dropping and recreating the messages table with the new constraint
-- SQLite doesn't support ALTER TABLE to modify CHECK constraints

-- Step 1: Create a new messages table with the updated constraint
CREATE TABLE messages_new (
    message_id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    sequence_id INTEGER NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('user', 'agent', 'tool', 'system', 'error', 'gitinfo', 'startup-hook')),
    llm_data TEXT, -- JSON data sent to/from LLM
    user_data TEXT, -- JSON data for UI display
    usage_data TEXT, -- JSON data about token usage, etc.
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    display_data TEXT, -- JSON data for display purposes
    FOREIGN KEY (conversation_id) REFERENCES conversations(conversation_id) ON DELETE CASCADE
);

-- Step 2: Copy data from old table to new table
INSERT INTO messages_new (message_id, conversation_id, sequence_id, type, llm_data, user_data, usage_data, created_at, display_data)
SELECT message_id, conversation_id, sequence_id, type, llm_data, user_data, usage_data, created_at, display_data FROM messages;

-- Step 3: Drop the old table
DROP TABLE messages;

-- Step 4: Rename the new table
ALTER TABLE messages_new RENAME TO messages;

-- Step 5: Recreate indexes
CREATE INDEX idx_messages_conversation_id ON messages(conversation_id);
CREATE INDEX idx_messages_conversation_sequence ON messages(conversation_id, sequence_id);
CREATE INDEX idx_messages_type ON messages(type);
