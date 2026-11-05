-- name: CreateConversationVM :exec
INSERT INTO conversation_vms (conversation_id, vm_name, base_vm, integration, shared, status, dir, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetConversationVM :one
-- The current VM of the conversation, or of the top-level conversation it
-- is a subagent of. A deleted conversation counts as archived.
WITH RECURSIVE chain(id, depth) AS (
    SELECT CAST(sqlc.arg(conversation_id) AS TEXT), 0
    UNION ALL
    SELECT c.parent_conversation_id, chain.depth + 1 FROM conversations c JOIN chain ON c.conversation_id = chain.id
    WHERE c.parent_conversation_id IS NOT NULL AND chain.depth < 64
)
SELECT sqlc.embed(v), CAST(COALESCE(c.archived, 1) AS BOOLEAN) AS conversation_archived
FROM conversation_vms v
LEFT JOIN conversations c ON c.conversation_id = v.conversation_id
JOIN chain ON v.conversation_id = chain.id AND v.replaced_at = ''
ORDER BY chain.depth DESC LIMIT 1;

-- name: ListConversationVMs :many
-- Every conversation VM, current or replaced. A deleted conversation counts
-- as archived.
SELECT sqlc.embed(v), CAST(COALESCE(c.archived, 1) AS BOOLEAN) AS conversation_archived
FROM conversation_vms v
LEFT JOIN conversations c ON c.conversation_id = v.conversation_id
ORDER BY v.created_at, v.id;

-- name: ListCurrentConversationVMs :many
-- The conversation VMs that exist, current or replaced, or that an active
-- conversation copies afresh at its next turn. A deleted conversation counts
-- as archived.
SELECT sqlc.embed(v), CAST(COALESCE(c.archived, 1) AS BOOLEAN) AS conversation_archived
FROM conversation_vms v
LEFT JOIN conversations c ON c.conversation_id = v.conversation_id
WHERE v.status != 'deleted' OR v.replaced_at = '' AND NOT COALESCE(c.archived, 1)
ORDER BY v.created_at, v.id;

-- name: UpdateConversationVM :execrows
UPDATE conversation_vms
SET status = ?, error = ?, ssh_dest = ?, https_url = ?, vm_created_at = ?, copy_requested_at = ?, dir = ?, updated_at = ?
WHERE id = ?;

-- name: GetConversationVMByID :one
-- A deleted conversation counts as archived.
SELECT sqlc.embed(v), CAST(COALESCE(c.archived, 1) AS BOOLEAN) AS conversation_archived
FROM conversation_vms v
LEFT JOIN conversations c ON c.conversation_id = v.conversation_id
WHERE v.id = ?;

-- name: ReplaceConversationVM :execrows
-- Keeps the conversation's current VM as one it left.
UPDATE conversation_vms SET replaced_at = ?, updated_at = ?
WHERE conversation_id = ? AND replaced_at = '';

-- name: DeleteCurrentConversationVM :execrows
-- Forgets the conversation's current VM, which Shelley has nothing to clean
-- up on.
DELETE FROM conversation_vms WHERE conversation_id = ? AND replaced_at = '';

-- name: OwnConversationVMNameUsed :one
SELECT CAST(EXISTS (SELECT 1 FROM conversation_vms WHERE vm_name = ? AND NOT shared) AS BOOLEAN) AS used;
