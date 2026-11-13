-- name: GetConversationThemes :one
SELECT * FROM conversation_themes WHERE conversation_id = ?;

-- name: ListConversationThemes :many
SELECT ct.conversation_id, ct.chapters_json
FROM conversation_themes ct
JOIN conversations c ON c.conversation_id = ct.conversation_id
WHERE c.parent_conversation_id IS NULL
  AND c.is_draft = FALSE
  AND json_extract(c.conversation_options, '$.kind') IS NOT 'search';

-- name: UpsertConversationThemes :exec
INSERT INTO conversation_themes (conversation_id, chapters_json, through_message_id)
VALUES (?, ?, ?)
ON CONFLICT (conversation_id) DO UPDATE SET
    chapters_json = excluded.chapters_json,
    through_message_id = excluded.through_message_id;
