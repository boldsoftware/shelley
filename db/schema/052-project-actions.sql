-- A project's actions: commands a conversation in it can run, supervised by
-- the server, one at a time per conversation.
ALTER TABLE profile_projects ADD COLUMN actions TEXT NOT NULL DEFAULT '[]';

-- profile is the project the run started in; it outlives a later move.
-- command is the one the run executed; archived, whether its success
-- archived the conversation. report is the run's report to the
-- conversation's agent: 'pending' until decided, then 'sent' or 'skipped'.
CREATE TABLE action_runs (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) ON DELETE CASCADE,
    profile TEXT NOT NULL,
    action_id TEXT NOT NULL,
    label TEXT NOT NULL,
    command TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'interrupted')),
    output TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    finished_at TEXT,
    exit_code INTEGER,
    archive_on_success BOOLEAN NOT NULL,
    archived BOOLEAN NOT NULL DEFAULT FALSE,
    report TEXT NOT NULL CHECK (report IN ('pending', 'sent', 'skipped'))
);
CREATE UNIQUE INDEX one_running_action ON action_runs(conversation_id) WHERE status = 'running';
CREATE INDEX action_runs_conversation ON action_runs(conversation_id, started_at);
CREATE INDEX action_runs_report_pending ON action_runs(id) WHERE report = 'pending';
