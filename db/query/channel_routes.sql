-- name: CreateChannelRoute :exec
-- Recording a route again, as a retried delivery does, keeps the first.
INSERT INTO channel_message_routes (conversation_id, external_message_id, chat_id, endpoint)
VALUES (?, ?, ?, ?)
ON CONFLICT DO NOTHING;

-- name: GetChannelRoute :one
SELECT * FROM channel_message_routes WHERE conversation_id = ? AND external_message_id = ?;

-- name: LatestChannelRoute :one
SELECT * FROM channel_message_routes WHERE chat_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 1;

-- name: HasChannelRouteEndpoint :one
SELECT CAST(EXISTS (SELECT 1 FROM channel_message_routes WHERE chat_id = ? AND endpoint = ?) AS BOOLEAN);

-- name: LatestChannelRouteWithChatPrefix :one
-- The latest route of conversation_id whose chat id starts with prefix, such
-- as a Slack thread's.
SELECT * FROM channel_message_routes
WHERE conversation_id = @conversation_id AND substr(chat_id, 1, length(CAST(@prefix AS TEXT))) = CAST(@prefix AS TEXT)
ORDER BY created_at DESC, rowid DESC LIMIT 1;
