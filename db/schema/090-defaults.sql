-- Defaults: JSON values by key, a store clients keep their settings in
-- (GET/POST /api/defaults). Shelley itself never reads them. version
-- counts a key's changes, for compare-and-swap; a deleted key keeps its
-- row with a NULL value, so that if it's made again its versions go on
-- rather than repeat. The times are to the millisecond, and updated_at
-- moves on every change.
CREATE TABLE defaults (
    key TEXT PRIMARY KEY,
    value TEXT,
    version INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now'))
);
