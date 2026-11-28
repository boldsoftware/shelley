-- name: RecordCheckoutActivity :exec
INSERT INTO checkout_activity (conversation_id, machine, worktree, touched_at, edited)
VALUES (?, ?, ?, ?, TRUE)
ON CONFLICT (conversation_id, machine, worktree) DO UPDATE SET
    touched_at = CASE WHEN edited THEN max(touched_at, excluded.touched_at) ELSE excluded.touched_at END,
    edited = TRUE;

-- name: RecentCheckouts :many
WITH RECURSIVE chats(id) AS (
    SELECT CAST(sqlc.arg(chat) AS TEXT)
    UNION ALL
    SELECT c.conversation_id FROM conversations c JOIN chats ON c.parent_conversation_id = chats.id
), activity(worktree, touched_at) AS (
    SELECT a.worktree, a.touched_at FROM chats CROSS JOIN checkout_activity a ON a.conversation_id = chats.id
    WHERE a.machine = sqlc.arg(machine) AND a.edited
    UNION ALL
    SELECT CAST(json_extract(m.user_data, '$.worktree') AS TEXT),
           CAST(unixepoch(m.created_at, 'subsec') * 1000000000 AS INTEGER)
    FROM chats CROSS JOIN messages m ON m.conversation_id = chats.id
    WHERE m.type = 'gitinfo'
      AND json_extract(m.user_data, '$.commit') != ''
      AND json_extract(m.user_data, '$.worktree') != ''
      AND coalesce(json_extract(m.user_data, '$.machine'), '') = sqlc.arg(machine)
      AND NOT coalesce(json_extract(m.user_data, '$.tour_request'), FALSE)
)
SELECT CAST(worktree AS TEXT) AS worktree FROM activity
GROUP BY worktree ORDER BY max(touched_at) DESC, worktree;
