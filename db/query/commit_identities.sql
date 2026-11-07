-- name: UpsertProjectCommitIdentity :exec
INSERT INTO profile_commit_identities (profile, name, email) VALUES (?, ?, ?)
ON CONFLICT (profile) DO UPDATE SET name = excluded.name, email = excluded.email;

-- name: DeleteProjectCommitIdentity :exec
DELETE FROM profile_commit_identities WHERE profile = ?;

-- name: ListProjectCommitIdentities :many
SELECT * FROM profile_commit_identities;

-- name: GetConversationCommitIdentity :one
-- The commit identity of the project of a conversation's top-level
-- conversation (subagents work for it).
WITH RECURSIVE up(id, parent) AS (
    SELECT s.conversation_id, s.parent_conversation_id FROM conversations s WHERE s.conversation_id = sqlc.arg(conversation_id)
    UNION ALL
    SELECT p.conversation_id, p.parent_conversation_id FROM conversations p JOIN up ON p.conversation_id = up.parent
)
SELECT i.name, i.email FROM up
JOIN conversations top ON top.conversation_id = up.id
JOIN profile_commit_identities i
    ON i.profile = CASE WHEN json_valid(top.conversation_options) THEN json_extract(top.conversation_options, '$.profile') END
WHERE up.parent IS NULL;
