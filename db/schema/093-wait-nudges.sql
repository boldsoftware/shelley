-- Wait nudges (see server/wait_nudges.go): a conversation waiting on
-- delegated work that runs on without news (a background job still running,
-- a subagent gone silent) hears about it at growing intervals. A row is the
-- latest wait on a subject, which began at since, and how many nudges went
-- out about it, so that a restart neither repeats a nudge nor skips one that
-- came due meanwhile.
CREATE TABLE wait_nudges (
    conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) ON DELETE CASCADE,
    -- 'background_job' or 'subagent'.
    kind TEXT NOT NULL,
    -- The job's ID, or the subagent's conversation ID.
    subject TEXT NOT NULL,
    since DATETIME NOT NULL,
    sent INTEGER NOT NULL,
    PRIMARY KEY (conversation_id, kind, subject)
);
