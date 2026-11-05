-- A project gets a stable id, kept across renames and never reused, and its
-- position in the user's order of projects (0 first). Existing projects keep
-- their rowids as ids and the order they were listed in, by name. The view
-- reading profile_projects is dropped and recreated around the rebuild (see
-- 060-workspace-views.sql).
DROP VIEW workspace_conversations;

CREATE TABLE profile_projects_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    profile TEXT NOT NULL UNIQUE REFERENCES profiles(name) ON UPDATE CASCADE ON DELETE CASCADE,
    instructions TEXT NOT NULL DEFAULT '',
    cwd TEXT NOT NULL DEFAULT '',
    actions TEXT NOT NULL DEFAULT '[]',
    position INTEGER NOT NULL
);
INSERT INTO profile_projects_new (id, profile, instructions, cwd, actions, position)
SELECT rowid, profile, instructions, cwd, actions, ROW_NUMBER() OVER (ORDER BY profile) - 1
FROM profile_projects;
DROP TABLE profile_projects;
ALTER TABLE profile_projects_new RENAME TO profile_projects;

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
