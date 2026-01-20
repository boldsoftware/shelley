-- Add parent_conversation_id column for subagent conversations
ALTER TABLE conversations ADD COLUMN parent_conversation_id TEXT REFERENCES conversations(conversation_id);

-- Index for efficient parent-child lookups
CREATE INDEX idx_conversations_parent_id ON conversations(parent_conversation_id) WHERE parent_conversation_id IS NOT NULL;
