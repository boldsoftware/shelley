-- The Assistant is no longer a stored id but the most recently created
-- top-level, unarchived, non-draft conversation with the Assistant profile,
-- whether or not the profile exists, that is not bound to a chat (texted
-- chats' conversations had the profile): creating a conversation with it
-- starts a new Assistant, and the old one stays an ordinary conversation.
--
-- A project named Assistant, possible once the Assistant profile was
-- deleted, has that profile, so its conversations would be Assistants. It
-- takes a free name, "Assistant project" or the first numbered one, and
-- its conversations go with it, as do the profiles their markers saved for
-- forks from before them to go back to (server.applyForkPointModelState).
CREATE TEMP TABLE assistant_project AS
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 99),
names(name, i) AS (SELECT 'Assistant project' || CASE WHEN i > 1 THEN ' ' || i ELSE '' END, i FROM n)
SELECT name FROM names
WHERE EXISTS (SELECT 1 FROM profile_projects WHERE profile = 'Assistant')
  AND name NOT IN (SELECT name FROM profiles)
ORDER BY i LIMIT 1;
UPDATE conversations
SET conversation_options = json_set(conversation_options, '$.profile', (SELECT name FROM assistant_project))
WHERE EXISTS (SELECT 1 FROM assistant_project)
  AND conversation_id NOT IN (SELECT assistant_id FROM workspace)
  AND CASE WHEN json_valid(conversation_options) THEN json_extract(conversation_options, '$.profile') END = 'Assistant';
UPDATE messages
SET user_data = json_set(user_data, '$.previous.profile', (SELECT name FROM assistant_project))
WHERE type = 'modelchange'
  AND EXISTS (SELECT 1 FROM assistant_project)
  AND conversation_id NOT IN (SELECT assistant_id FROM workspace)
  AND CASE WHEN json_valid(user_data) THEN json_extract(user_data, '$.previous.profile') END = 'Assistant';
UPDATE profiles SET name = (SELECT name FROM assistant_project)
WHERE name = 'Assistant' AND EXISTS (SELECT 1 FROM assistant_project);
DROP TABLE assistant_project;

-- The current Assistant takes the label, keeping its own settings rather
-- than following the profile's.
UPDATE conversations
SET conversation_options = json_set(
    CASE WHEN NOT json_valid(conversation_options) THEN '{}'
        WHEN json_type(conversation_options) = 'object' THEN conversation_options ELSE '{}' END,
    '$.profile', 'Assistant', '$.settings_edited', json('true'))
WHERE conversation_id IN (SELECT assistant_id FROM workspace)
  AND CASE WHEN json_valid(conversation_options) THEN json_extract(conversation_options, '$.profile') END IS NOT 'Assistant';

-- With the Assistant profile the default, conversations that named no
-- profile took its label, so it says nothing about them: only the current
-- Assistant keeps it, in its markers too. They keep their settings.
UPDATE conversations
SET conversation_options = json_remove(conversation_options, '$.profile')
WHERE EXISTS (SELECT 1 FROM profiles WHERE name = 'Assistant' AND is_default)
  AND conversation_id NOT IN (SELECT assistant_id FROM workspace)
  AND CASE WHEN json_valid(conversation_options) THEN json_extract(conversation_options, '$.profile') END = 'Assistant';
UPDATE messages
SET user_data = json_remove(user_data, '$.previous.profile')
WHERE type = 'modelchange'
  AND EXISTS (SELECT 1 FROM profiles WHERE name = 'Assistant' AND is_default)
  AND conversation_id NOT IN (SELECT assistant_id FROM workspace)
  AND CASE WHEN json_valid(user_data) THEN json_extract(user_data, '$.previous.profile') END = 'Assistant';

-- New conversations that name no profile take the default one's, so it
-- can't be the Assistant profile. Default is the default instead, restored
-- if it was deleted. If a project has the name (a project's profile can't
-- be the default), a new "Default 2", or the first free number, is.
INSERT INTO profiles (name, builtin, user_edited)
SELECT 'Default', TRUE, FALSE WHERE EXISTS (SELECT 1 FROM profiles WHERE name = 'Assistant' AND is_default)
  AND NOT EXISTS (SELECT 1 FROM profiles WHERE name = 'Default');
WITH RECURSIVE n(i) AS (SELECT 2 UNION ALL SELECT i + 1 FROM n WHERE i < 99)
INSERT INTO profiles (name)
SELECT 'Default ' || i FROM n
WHERE EXISTS (SELECT 1 FROM profiles WHERE name = 'Assistant' AND is_default)
  AND EXISTS (SELECT 1 FROM profile_projects WHERE profile = 'Default')
  AND 'Default ' || i NOT IN (SELECT name FROM profiles)
ORDER BY i LIMIT 1;
UPDATE profiles SET is_default = FALSE WHERE name = 'Assistant';
UPDATE profiles SET is_default = TRUE
WHERE NOT EXISTS (SELECT 1 FROM profiles WHERE is_default)
  AND name = (SELECT name FROM profiles WHERE (name = 'Default' OR name GLOB 'Default [0-9]*')
    AND name NOT IN (SELECT profile FROM profile_projects) ORDER BY name = 'Default' DESC, rowid DESC LIMIT 1);

-- The tools the Assistant answers with (db.AssistantTools) are now settings
-- like any other: the Slack tools are no longer on by default, nor is
-- message_user kept on for the Assistant. So conversations with the
-- profile, and the profile, turn them on unless they chose otherwise. The
-- built-in profile, unless the user edited it, gets its shipped settings at
-- startup anyway.
UPDATE conversations
SET conversation_options = json_set(conversation_options, '$.tool_overrides', json_patch(
    '{"message_user":"on","send_to_thread":"on","send_to_channel":"on","add_reaction":"on"}',
    CASE WHEN json_type(conversation_options, '$.tool_overrides') = 'object' THEN json_extract(conversation_options, '$.tool_overrides') ELSE '{}' END))
WHERE parent_conversation_id IS NULL
  AND CASE WHEN json_valid(conversation_options) THEN json_extract(conversation_options, '$.profile') END = 'Assistant';
UPDATE profiles
SET settings = json_set(settings, '$.tool_overrides', json_patch(
    '{"message_user":"on","send_to_thread":"on","send_to_channel":"on","add_reaction":"on"}',
    CASE WHEN json_type(settings, '$.tool_overrides') = 'object' THEN json_extract(settings, '$.tool_overrides') ELSE '{}' END))
WHERE name = 'Assistant' AND CASE WHEN json_valid(settings) THEN json_type(settings) = 'object' END;
-- So do the settings that markers saved for forks.
UPDATE messages
SET user_data = json_set(user_data, '$.previous.tool_overrides', json_patch(
    '{"message_user":"on","send_to_thread":"on","send_to_channel":"on","add_reaction":"on"}',
    CASE WHEN json_type(user_data, '$.previous.tool_overrides') = 'object' THEN json_extract(user_data, '$.previous.tool_overrides') ELSE '{}' END))
WHERE type = 'modelchange'
  AND CASE WHEN json_valid(user_data) THEN json_extract(user_data, '$.previous.profile') END = 'Assistant';

-- disable_notifications is now a setting too, rather than the Assistant
-- profile's name keeping turn ends unannounced. Markers save it where it
-- was in effect: with the Assistant profile, or where the conversation
-- turned it off.
UPDATE messages
SET user_data = json_set(user_data, '$.previous.disable_notifications', json('true'))
WHERE type = 'modelchange'
  AND CASE WHEN json_valid(user_data) THEN json_type(user_data, '$.previous') = 'object' END
  AND (json_extract(user_data, '$.previous.profile') = 'Assistant'
    OR (SELECT CASE WHEN json_valid(c.conversation_options) THEN json_extract(c.conversation_options, '$.disable_notifications') END
      FROM conversations c WHERE c.conversation_id = messages.conversation_id));
-- A conversation that turned it off would lose that by following its
-- profile; it keeps its settings instead.
UPDATE conversations
SET conversation_options = json_set(conversation_options, '$.settings_edited', json('true'))
WHERE CASE WHEN json_valid(conversation_options) THEN json_extract(conversation_options, '$.disable_notifications')
  AND COALESCE(json_extract(conversation_options, '$.profile'), '') NOT IN ('', 'Assistant') END;
UPDATE conversations
SET conversation_options = json_set(conversation_options, '$.disable_notifications', json('true'))
WHERE parent_conversation_id IS NULL
  AND CASE WHEN json_valid(conversation_options) THEN json_extract(conversation_options, '$.profile') END = 'Assistant';
UPDATE profiles
SET settings = json_set(settings, '$.disable_notifications', json('true'))
WHERE name = 'Assistant' AND CASE WHEN json_valid(settings) THEN json_type(settings) = 'object' END;

DROP VIEW workspace_assistant;
DROP TABLE workspace;

-- workspace_conversations reads this view by name, so it needs no change.
CREATE VIEW workspace_assistant AS
SELECT conversation_id AS assistant_id FROM conversations
WHERE parent_conversation_id IS NULL AND NOT archived AND NOT is_draft AND external_conversation_id IS NULL
  AND CASE WHEN json_valid(conversation_options) THEN json_extract(conversation_options, '$.profile') END = 'Assistant'
ORDER BY created_at DESC, rowid DESC
LIMIT 1;

-- Every commit checks for a new Assistant (server/assistant_watch.go).
CREATE INDEX idx_conversations_assistant ON conversations(created_at)
WHERE parent_conversation_id IS NULL AND NOT archived AND NOT is_draft AND external_conversation_id IS NULL
  AND CASE WHEN json_valid(conversation_options) THEN json_extract(conversation_options, '$.profile') END = 'Assistant';

-- A chat's retry finds the conversation that took its message
-- (server.channelMessageTaker).
CREATE INDEX channel_message_routes_message ON channel_message_routes(chat_id, external_message_id);
