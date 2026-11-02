-- name: GetWorkspace :one
SELECT * FROM workspace WHERE singleton = 1;

-- name: CreateWorkspace :exec
INSERT INTO workspace (singleton, assistant_id) VALUES (1, ?);

-- name: WorkspaceMode :one
SELECT CAST(EXISTS (SELECT 1 FROM feature_flags WHERE name = 'workspace' AND trim(value) = 'true') AS BOOLEAN);

-- name: GetAssistantID :one
SELECT assistant_id FROM workspace_assistant;

-- name: GetWorkspaceConversation :one
SELECT * FROM workspace_conversations WHERE conversation_id = ?;

-- name: ListWorkspaceConversations :many
SELECT * FROM workspace_conversations ORDER BY conversation_id;

-- name: HasDependentWork :one
-- Whether a subagent of the conversation (at any depth) is working,
-- interrupted, or has queued messages, or a background job in the family
-- is running or unreported.
WITH RECURSIVE family(id) AS (
    SELECT CAST(sqlc.arg(conversation_id) AS TEXT)
    UNION SELECT c.conversation_id FROM conversations c JOIN family f ON c.parent_conversation_id = f.id
)
SELECT CAST(EXISTS (
    SELECT 1 FROM conversations c JOIN family f ON c.conversation_id = f.id
    WHERE c.conversation_id != sqlc.arg(conversation_id)
      AND (c.agent_working OR c.turn_interrupted OR c.queued_messages NOT IN ('[]', '', 'null'))
    UNION ALL
    SELECT 1 FROM background_jobs b JOIN family f ON b.conversation_id = f.id WHERE NOT b.exited OR NOT b.notified
) AS BOOLEAN);

-- name: SetConversationProfile :exec
UPDATE conversations
SET conversation_options = json_set(
    CASE WHEN json_valid(conversation_options) THEN conversation_options ELSE '{}' END,
    '$.profile', CAST(sqlc.arg(profile) AS TEXT))
WHERE conversation_id = sqlc.arg(conversation_id);

-- name: RenameConversationsProfile :exec
UPDATE conversations
SET conversation_options = json_set(conversation_options, '$.profile', CAST(sqlc.arg(to_profile) AS TEXT))
WHERE CASE WHEN json_valid(conversation_options) THEN json_extract(conversation_options, '$.profile') END = sqlc.arg(from_profile);

-- name: HasRunningAction :one
SELECT CAST(EXISTS (SELECT 1 FROM action_runs WHERE conversation_id = ? AND status = 'running') AS BOOLEAN);

-- name: HasAnyRunningAction :one
SELECT CAST(EXISTS (SELECT 1 FROM action_runs WHERE status = 'running') AS BOOLEAN);

-- name: CreateActionRun :exec
INSERT INTO action_runs (id, conversation_id, profile, action_id, label, command, status, started_at, archive_on_success, report)
VALUES (?, ?, ?, ?, ?, ?, 'running', ?, ?, ?);

-- name: GetActionRun :one
SELECT * FROM action_runs WHERE id = ?;

-- name: ListDueActionReports :many
SELECT * FROM action_runs WHERE status != 'running' AND report = 'pending' ORDER BY started_at, id;

-- name: ListWorkspaceActionRuns :many
-- Running runs, recent ones, and each conversation's latest, which its
-- transcript shows until it reports. GET /api/action-runs/{id} has the rest.
SELECT id, conversation_id, profile, action_id, label, status, started_at, finished_at, exit_code, report
FROM action_runs
WHERE status = 'running' OR id IN (
    SELECT id FROM action_runs WHERE status != 'running' ORDER BY started_at DESC, id DESC LIMIT 20
) OR id = (
    SELECT latest.id FROM action_runs latest
    WHERE latest.conversation_id = action_runs.conversation_id
    ORDER BY latest.started_at DESC, latest.id DESC LIMIT 1
)
ORDER BY started_at, id;

-- name: SetActionRunOutput :exec
UPDATE action_runs SET output = ? WHERE id = ?;

-- name: FinishActionRun :exec
UPDATE action_runs SET status = ?, error = ?, finished_at = ?, exit_code = ? WHERE id = ?;

-- name: MarkActionRunArchived :exec
UPDATE action_runs SET archived = TRUE WHERE id = ?;

-- name: InterruptRunningActionRuns :exec
UPDATE action_runs
SET status = 'interrupted', error = 'Server restarted; command outcome is unknown. The conversation was not archived.', finished_at = ?
WHERE status = 'running';

-- name: InterruptActionRun :execrows
UPDATE action_runs SET status = 'interrupted', error = ?, finished_at = ?
WHERE id = ? AND status = 'running';

-- name: SetPendingActionReport :execrows
UPDATE action_runs SET report = ? WHERE id = ? AND report = 'pending';

-- name: RenameActionRunsProfile :exec
UPDATE action_runs SET profile = sqlc.arg(to_profile) WHERE profile = sqlc.arg(from_profile);
