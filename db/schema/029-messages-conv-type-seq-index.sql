-- Index for the conversation-list preview extraction, which finds the most
-- recent agent message per listed conversation. The conversation list shows a
-- one-line preview = the text of the most recent agent message that has a
-- usable text block (in tool-calling turns the final text block is the
-- summary). That subquery is computed on read, scoped to the ~500 listed
-- conversations.
--
-- The existing idx_messages_conversation_sequence (conversation_id,
-- sequence_id) already lets the planner seek to a conversation's newest
-- messages, but it can't skip non-agent rows, so a conversation with a long
-- tail of user/tool messages forces a backwards scan. Adding type to the key
-- (conversation_id, type, sequence_id) lets the planner seek straight to each
-- conversation's agent messages newest-first regardless of interleaving.
CREATE INDEX IF NOT EXISTS idx_messages_conv_type_seq
  ON messages(conversation_id, type, sequence_id);

-- Seed the query planner's on-disk statistics for the new index. Creating
-- sqlite_stat1 here is also DDL, which bumps the schema cookie and forces
-- already-open connections to reload their cached stats on their next
-- statement.
ANALYZE;
