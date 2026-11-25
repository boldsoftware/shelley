-- name: GetWaitNudge :one
SELECT * FROM wait_nudges WHERE conversation_id = ? AND kind = ? AND subject = ?;

-- name: RecordWaitNudge :exec
INSERT INTO wait_nudges (conversation_id, kind, subject, since, sent) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (conversation_id, kind, subject) DO UPDATE SET since = excluded.since, sent = excluded.sent;
