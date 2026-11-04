-- name: InsertBackgroundJob :exec
INSERT INTO background_jobs (job_id, conversation_id, tool_use_id, command, pid, process_start_time, log_path, exit_path, started_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListUnfinishedBackgroundJobs :many
SELECT * FROM background_jobs WHERE NOT exited OR NOT notified ORDER BY started_at;

-- name: MarkBackgroundJobNotified :exec
-- Notified implies exited.
UPDATE background_jobs SET notified = TRUE, exited = TRUE WHERE job_id = ?;

-- name: MarkBackgroundJobExited :one
-- No row means the job was already marked exited.
UPDATE background_jobs SET exited = TRUE WHERE job_id = ? AND NOT exited RETURNING notified;

-- name: CountUnfinishedBackgroundJobs :one
-- A job is unfinished until its exit notice is queued in its conversation.
SELECT COUNT(*) FROM background_jobs WHERE conversation_id = ? AND (NOT exited OR NOT notified);

-- name: ListRunningBackgroundJobs :many
SELECT * FROM background_jobs WHERE conversation_id = ? AND NOT exited ORDER BY started_at;
