-- name: GetProject :one
SELECT * FROM profile_projects WHERE profile = ?;

-- name: ListProjects :many
SELECT * FROM profile_projects ORDER BY position;

-- name: ListProjectNames :many
SELECT profile FROM profile_projects ORDER BY position;

-- name: SetProjectPosition :exec
UPDATE profile_projects SET position = ? WHERE profile = ?;

-- name: CopyProfile :execrows
-- Creates profile name with the settings of profile from_profile, or of the
-- default profile if from_profile is empty.
INSERT INTO profiles (name, settings)
SELECT CAST(sqlc.arg(name) AS TEXT), settings FROM profiles
WHERE CASE WHEN CAST(sqlc.arg(from_profile) AS TEXT) = '' THEN is_default ELSE name = sqlc.arg(from_profile) END;

-- name: CreateProject :exec
-- A new project goes last.
INSERT INTO profile_projects (profile, instructions, cwd, actions, position)
VALUES (?, ?, ?, ?, (SELECT COALESCE(MAX(position) + 1, 0) FROM profile_projects));

-- name: UpdateProject :execrows
UPDATE profile_projects SET instructions = ?, cwd = ?, actions = ? WHERE profile = ?;

-- name: RenameProfile :exec
UPDATE profiles SET name = sqlc.arg(to_name) WHERE name = sqlc.arg(from_name);

-- name: DeleteProjectProfile :exec
DELETE FROM profiles WHERE name = ?;

-- name: GetProjectSetupConversationID :one
SELECT CAST(COALESCE(s.conversation_id, '') AS TEXT) FROM profile_projects p
LEFT JOIN profile_vm_setups s ON s.profile = p.profile
WHERE p.profile = ?;

-- name: GetProjectActivity :one
-- The project's unarchived conversations other than except_id, and whether
-- any of its conversations runs an action.
SELECT
    (SELECT COUNT(*) FROM workspace_conversations w
        WHERE w.project = sqlc.arg(project) AND NOT w.archived AND w.conversation_id != sqlc.arg(except_id)) AS active,
    CAST(EXISTS (
        SELECT 1 FROM action_runs r JOIN workspace_conversations w ON w.conversation_id = r.conversation_id
        WHERE w.project = sqlc.arg(project) AND r.status = 'running'
    ) AS BOOLEAN) AS running;

-- name: DeleteProjectRepos :exec
DELETE FROM profile_repos WHERE profile = ?;

-- name: CreateProjectRepo :exec
INSERT INTO profile_repos (profile, position, integration, repository) VALUES (?, ?, ?, ?);

-- name: ListProjectRepos :many
SELECT * FROM profile_repos ORDER BY profile, position;

-- name: UpsertProjectVM :exec
INSERT INTO profile_vms (profile, base_vm, shared_vm, integration) VALUES (?, ?, ?, ?)
ON CONFLICT (profile) DO UPDATE SET
    base_vm = excluded.base_vm, shared_vm = excluded.shared_vm, integration = excluded.integration;

-- name: DeleteProjectVM :exec
DELETE FROM profile_vms WHERE profile = ?;

-- name: ListProjectVMs :many
SELECT v.profile, v.base_vm, v.shared_vm, v.integration,
    CAST(COALESCE(s.conversation_id, '') AS TEXT) AS setup_conversation_id
FROM profile_vms v LEFT JOIN profile_vm_setups s ON s.profile = v.profile;

-- name: CreateProjectVMSetup :execrows
-- Records the conversation setting up the base VM of a project whose VMs
-- have no base or shared VM.
INSERT INTO profile_vm_setups (profile, conversation_id)
SELECT v.profile, CAST(sqlc.arg(conversation_id) AS TEXT) FROM profile_vms v
WHERE v.profile = sqlc.arg(profile) AND v.base_vm = '' AND v.shared_vm = '';

-- name: DeleteProjectVMSetup :exec
DELETE FROM profile_vm_setups WHERE profile = ?;

-- name: UpsertProjectBaseVM :exec
INSERT INTO profile_base_vms (integration, vm_name, profile, status, vm_created_at, requested_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (integration, vm_name) DO UPDATE SET
    profile = excluded.profile, status = excluded.status,
    vm_created_at = excluded.vm_created_at, requested_at = excluded.requested_at;

-- name: DeleteProjectBaseVM :exec
DELETE FROM profile_base_vms WHERE integration = ? AND vm_name = ?;

-- name: ListProjectBaseVMs :many
-- The base VMs of project, or of every project if it is empty.
SELECT * FROM profile_base_vms
WHERE CAST(sqlc.arg(project) AS TEXT) = '' OR profile = sqlc.arg(project)
ORDER BY requested_at, vm_name;
