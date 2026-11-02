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
