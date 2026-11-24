-- Secrets expire (docs/secrets.md): the server takes them out of messages
-- and keeps them a day. Ones stored before this get a day from now.
ALTER TABLE secrets ADD COLUMN expires_at DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00';
UPDATE secrets SET expires_at = datetime('now', '+1 day');
CREATE INDEX IF NOT EXISTS secrets_expires_at ON secrets(expires_at);
-- Nothing records which files secrets were written to any more.
DROP TABLE IF EXISTS secret_injections;
