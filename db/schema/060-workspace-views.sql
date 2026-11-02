-- The workspace's Assistant and conversations, shared by the queries that
-- need them (db/query/workspace.sql). Migrations that drop the columns
-- they read, or rebuild those tables, must drop and recreate them.

-- The Assistant's conversation id: none until workspace mode creates it, and
-- none while the "workspace" feature flag is off.
CREATE VIEW workspace_assistant AS
SELECT assistant_id FROM workspace
WHERE singleton = 1
  AND EXISTS (SELECT 1 FROM feature_flags WHERE name = 'workspace' AND trim(value) = 'true');

-- The workspace's conversations: top-level conversations whose profile is a
-- project, and the Assistant.
CREATE VIEW workspace_conversations AS
SELECT conversation_id, project, is_assistant, archived FROM (
    SELECT c.conversation_id,
        CAST(CASE WHEN c.conversation_id IN (SELECT assistant_id FROM workspace_assistant) THEN ''
            ELSE COALESCE(p.profile, '') END AS TEXT) AS project,
        CAST(c.conversation_id IN (SELECT assistant_id FROM workspace_assistant) AS BOOLEAN) AS is_assistant,
        c.archived
    FROM conversations c
    LEFT JOIN profile_projects p
        ON p.profile = CASE WHEN json_valid(c.conversation_options) THEN json_extract(c.conversation_options, '$.profile') END
    WHERE c.parent_conversation_id IS NULL
) WHERE project != '' OR is_assistant;
