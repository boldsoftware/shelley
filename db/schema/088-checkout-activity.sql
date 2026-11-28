CREATE TABLE checkout_activity (
    conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) ON DELETE CASCADE,
    machine TEXT NOT NULL,
    worktree TEXT NOT NULL,
    touched_at INTEGER NOT NULL,
    edited BOOLEAN NOT NULL,
    PRIMARY KEY (conversation_id, machine, worktree)
);
