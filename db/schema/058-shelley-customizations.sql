-- Shelley customization builds, established only by server-owned
-- verification, never chat output.
CREATE TABLE shelley_customizations (
    conversation_id TEXT PRIMARY KEY REFERENCES conversations(conversation_id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    generation TEXT NOT NULL,
    artifact_path TEXT NOT NULL DEFAULT '',
    data TEXT NOT NULL
);
CREATE UNIQUE INDEX shelley_customization_active ON shelley_customizations((1))
    WHERE status IN ('verifying', 'restarting');

-- Automatic restarts into installed builds. One row per event: 'attempted'
-- before restarting, 'failed' if that attempt did not exit, and 'cancelled'
-- when the user turned auto-restart off for the build. Any event for an
-- artifact means Shelley never auto-restarts into it again.
CREATE TABLE shelley_customization_auto_restarts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id TEXT NOT NULL,
    commit_sha TEXT NOT NULL,
    artifact_sha256 TEXT NOT NULL,
    event TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);
CREATE INDEX shelley_customization_auto_restarts_artifact ON shelley_customization_auto_restarts(artifact_sha256);
