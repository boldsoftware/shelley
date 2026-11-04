-- The Assistant no longer depends on the "workspace" feature flag, which
-- only changes how the UI shows it. workspace_conversations reads this view
-- by name, so it needs no change.
DROP VIEW workspace_assistant;

-- The Assistant's conversation id: none until it is first needed.
CREATE VIEW workspace_assistant AS
SELECT assistant_id FROM workspace WHERE singleton = 1;
