-- Each conversation's preview in the conversation list, shared by the list
-- and search queries (db/query/conversations.sql): the newest agent message
-- with text for the user, and its last such block. An agent writes the user
-- text in a text block, or with a tool whose input's text it sends:
-- message_user (the Assistant's chat and texts), send_to_thread or
-- send_to_channel (Slack). We trim the tools' text of ASCII whitespace, as
-- message_user trims it, so a reaction with blank text has none. With message_user on, the user sees
-- only what the agent sends (075's reply mark agrees), so text blocks don't
-- count.
--
-- The EXISTS stops at a message's first such block, and the ORDER BY rides
-- idx_messages_conv_type_seq, so we stop at the newest agent message with one
-- instead of extracting every agent message's blocks. jsonb_each hands each
-- block over as JSONB, which ->> reads in place; json_each would render it as
-- text for ->> to parse again. (jsonb_each needs SQLite 3.51: older sqlite3
-- shells can't read this view, or rename or drop columns or rename tables in
-- this database.)
--
-- preview_packed is the fixed-width RFC3339 timestamp (strftime
-- '%Y-%m-%dT%H:%M:%SZ', 20 bytes) followed by the text capped to 300 chars,
-- so we don't haul multi-KB replies across the driver + JSON + gzip for a
-- one-line UI field. db.splitPreviewPacked splits it back apart.
--
-- Migrations that rebuild messages or conversations must drop and recreate
-- this view.
CREATE VIEW conversation_previews AS
SELECT c.conversation_id, (
    SELECT strftime('%Y-%m-%dT%H:%M:%SZ', m.created_at) || substr((
        SELECT CASE je.value ->> 'Type'
                WHEN 2 THEN CASE WHEN NOT c.chat THEN je.value ->> 'Text' END
                WHEN 5 THEN CASE WHEN je.value ->> 'ToolName' IN ('message_user', 'send_to_thread', 'send_to_channel')
                    THEN trim(je.value ->> '$.ToolInput.text', char(9, 10, 11, 12, 13, 32)) END
            END AS reply
        FROM jsonb_each(m.llm_data, '$.Content') je
        WHERE reply <> ''
        ORDER BY je.key DESC LIMIT 1), 1, 300)
    FROM messages m
    WHERE m.conversation_id = c.conversation_id AND m.type = 'agent'
      AND EXISTS (
        SELECT CASE je.value ->> 'Type'
                WHEN 2 THEN CASE WHEN NOT c.chat THEN je.value ->> 'Text' END
                WHEN 5 THEN CASE WHEN je.value ->> 'ToolName' IN ('message_user', 'send_to_thread', 'send_to_channel')
                    THEN trim(je.value ->> '$.ToolInput.text', char(9, 10, 11, 12, 13, 32)) END
            END AS reply
        FROM jsonb_each(m.llm_data, '$.Content') je
        WHERE reply <> '')
    ORDER BY m.sequence_id DESC LIMIT 1) AS preview_packed
FROM (
    SELECT conversation_id, CASE WHEN json_valid(conversation_options)
            THEN conversation_options ->> '$.tool_overrides.message_user' END IS 'on' AS chat
    FROM conversations) c;
