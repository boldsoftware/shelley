-- name: GetConversationThemes :one
SELECT * FROM conversation_themes WHERE conversation_id = ?;

-- name: UpsertConversationThemes :exec
INSERT INTO conversation_themes (conversation_id, chapters_json, through_message_id)
VALUES (?, ?, ?)
ON CONFLICT (conversation_id) DO UPDATE SET
    chapters_json = excluded.chapters_json,
    through_message_id = excluded.through_message_id;
