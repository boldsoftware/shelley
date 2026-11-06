-- Secrets captured from the composer (docs/secrets.md). Messages
-- hold only {{secret:ID}}; the value lives here, sealed with AES-GCM under
-- a key file beside the database.
CREATE TABLE IF NOT EXISTS secrets (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    sealed BLOB NOT NULL,
    conversation_id TEXT REFERENCES conversations(conversation_id) ON DELETE SET NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Each time the write_secret tool put a secret in a file.
CREATE TABLE IF NOT EXISTS secret_injections (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    secret_id TEXT NOT NULL REFERENCES secrets(id) ON DELETE CASCADE,
    conversation_id TEXT REFERENCES conversations(conversation_id) ON DELETE SET NULL,
    machine TEXT NOT NULL,
    path TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS secret_injections_secret ON secret_injections(secret_id);
