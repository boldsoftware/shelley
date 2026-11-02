-- name: CreateConversationVM :exec
INSERT INTO conversation_vms (conversation_id, vm_name, base_vm, integration, shared, status, dir, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetConversationVM :one
-- The VM of the conversation, or of the top-level conversation it is a
-- subagent of. A deleted conversation counts as archived.
WITH RECURSIVE chain(id, depth) AS (
    SELECT CAST(sqlc.arg(conversation_id) AS TEXT), 0
    UNION ALL
    SELECT c.parent_conversation_id, chain.depth + 1 FROM conversations c JOIN chain ON c.conversation_id = chain.id
    WHERE c.parent_conversation_id IS NOT NULL AND chain.depth < 64
)
SELECT sqlc.embed(v), CAST(COALESCE(c.archived, 1) AS BOOLEAN) AS conversation_archived
FROM conversation_vms v
LEFT JOIN conversations c ON c.conversation_id = v.conversation_id
JOIN chain ON v.conversation_id = chain.id
ORDER BY chain.depth DESC LIMIT 1;

-- name: ListConversationVMs :many
-- A deleted conversation counts as archived.
SELECT sqlc.embed(v), CAST(COALESCE(c.archived, 1) AS BOOLEAN) AS conversation_archived
FROM conversation_vms v
LEFT JOIN conversations c ON c.conversation_id = v.conversation_id
ORDER BY v.created_at, v.conversation_id;

-- name: ListCurrentConversationVMs :many
-- The conversation VMs that exist or that an active conversation copies
-- afresh at its next turn. A deleted conversation counts as archived.
SELECT sqlc.embed(v), CAST(COALESCE(c.archived, 1) AS BOOLEAN) AS conversation_archived
FROM conversation_vms v
LEFT JOIN conversations c ON c.conversation_id = v.conversation_id
WHERE v.status != 'deleted' OR NOT COALESCE(c.archived, 1)
ORDER BY v.created_at, v.conversation_id;

-- name: UpdateConversationVM :execrows
UPDATE conversation_vms
SET status = ?, error = ?, ssh_dest = ?, https_url = ?, vm_created_at = ?, copy_requested_at = ?, dir = ?, updated_at = ?
WHERE conversation_id = ?;
