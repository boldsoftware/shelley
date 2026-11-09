DROP TRIGGER record_conversation_reply;

CREATE TRIGGER record_conversation_reply AFTER INSERT ON messages
WHEN NEW.forked_from_message_id IS NULL
  AND (NEW.type = 'error' OR NOT EXISTS (
    SELECT 1 FROM conversations
    WHERE conversation_id = NEW.conversation_id
      AND CASE WHEN json_valid(conversation_options)
        THEN json_extract(conversation_options, '$.tool_overrides.message_user') END = 'on'
  ))
  AND (
  (NEW.type IN ('agent', 'error') AND json_valid(NEW.llm_data) AND EXISTS (
      SELECT 1 FROM json_each(NEW.llm_data, '$.Content') AS content
      WHERE content.type = 'object' AND json_extract(content.value, '$.Type') = 2
        AND (trim(COALESCE(json_extract(content.value, '$.Text'), '')) != ''
             OR COALESCE(json_extract(content.value, '$.MediaType'), '') != '')
    ))
  OR
  (NEW.type = 'user' AND json_valid(NEW.user_data)
    AND trim(COALESCE(json_extract(NEW.user_data, '$.Text'), '')) != ''
    AND (
      (COALESCE(json_extract(NEW.user_data, '$.sender_relationship'), '') IN ('subagent', 'parent')
        AND COALESCE(json_extract(NEW.user_data, '$.sender_conversation_id'), '') != '')
      OR COALESCE(json_extract(NEW.user_data, '$.background_job_id'), '') != ''
    ))
)
BEGIN
    INSERT INTO conversation_marks(kind, conversation_id, marked_at)
    VALUES ('reply', NEW.conversation_id, (SELECT marked_at FROM next_conversation_mark))
    ON CONFLICT (kind, user_id, conversation_id) DO UPDATE SET marked_at = excluded.marked_at;
END;
