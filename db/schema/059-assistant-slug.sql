-- The workspace Assistant is titled "Assistant", not after its first message:
-- its slug is "assistant", or none while another conversation has that slug.
UPDATE conversations SET slug = CASE
    WHEN EXISTS(SELECT 1 FROM conversations o WHERE o.slug = 'assistant' AND o.conversation_id != conversations.conversation_id) THEN NULL
    ELSE 'assistant'
END
WHERE conversation_id = (SELECT assistant_id FROM workspace WHERE singleton = 1);
