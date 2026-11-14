-- Slack events now go to the Assistant (server/slack_channel.go), which
-- answers them with the Slack tools. The Slack Assistant's conversation is
-- archived with its history, taking no more Slack events nor resuming a
-- turn an upgrade interrupted, and its profile, unless the user made it
-- their own, goes.
UPDATE conversations SET external_conversation_id = NULL, external_endpoint = NULL, archived = TRUE, agent_working = FALSE
WHERE external_conversation_id = 'slack:assistant';

DELETE FROM builtin_profiles WHERE name = 'Slack Assistant';
UPDATE profiles SET builtin = FALSE WHERE name = 'Slack Assistant';
DELETE FROM profiles WHERE name = 'Slack Assistant' AND NOT user_edited AND NOT is_default
  AND NOT EXISTS (SELECT 1 FROM profile_projects WHERE profile = 'Slack Assistant')
  AND NOT EXISTS (SELECT 1 FROM profile_base_vms WHERE profile = 'Slack Assistant');
