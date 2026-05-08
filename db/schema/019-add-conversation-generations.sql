-- Add conversation generations. generation 1 is the initial generation.
ALTER TABLE conversations ADD COLUMN current_generation INTEGER NOT NULL DEFAULT 1;
ALTER TABLE messages ADD COLUMN generation INTEGER NOT NULL DEFAULT 1;

CREATE INDEX idx_messages_conversation_generation_context_sequence ON messages(conversation_id, generation, excluded_from_context, sequence_id);
