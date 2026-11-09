-- Tool results carry a call ID. Match it to the preceding agent turn so only
-- a successfully delivered message_user response advances the reply mark.
CREATE TRIGGER record_message_user_reply AFTER INSERT ON messages
WHEN NEW.forked_from_message_id IS NULL AND NEW.type IN ('user', 'tool')
  AND json_valid(NEW.llm_data) AND EXISTS (
    SELECT 1 FROM json_each(NEW.llm_data, '$.Content') AS result
    WHERE result.type = 'object' AND result.value ->> '$.Type' = 6
      AND NOT (result.value ->> '$.ToolError')
      AND EXISTS (
        SELECT 1 FROM json_each((
          SELECT CASE WHEN json_valid(m.llm_data) THEN m.llm_data END FROM messages m
          WHERE m.conversation_id = NEW.conversation_id AND m.type = 'agent'
            AND m.sequence_id < NEW.sequence_id
          ORDER BY m.sequence_id DESC LIMIT 1
        ), '$.Content') AS call
        WHERE call.type = 'object' AND call.value ->> '$.Type' = 5
          AND call.value ->> '$.ToolName' = 'message_user'
          AND call.value ->> '$.ID' = result.value ->> '$.ToolUseID'
      )
  )
BEGIN
  INSERT INTO conversation_marks(kind, conversation_id, marked_at)
  VALUES ('reply', NEW.conversation_id, (SELECT marked_at FROM next_conversation_mark))
  ON CONFLICT (kind, user_id, conversation_id) DO UPDATE SET marked_at = excluded.marked_at;
END;
