-- sqlc-only stub. The real messages_fts is a CREATE VIRTUAL TABLE USING
-- fts5(...) (db/schema/021-messages-fts.sql). sqlc's parser ignores the
-- CREATE but seems to create a placeholder relation. We add the columns
-- sqlc needs to type-check our queries (the FTS5 `rank` column and the
-- table-self column used in `messages_fts MATCH ?`).
ALTER TABLE messages_fts ADD COLUMN rank REAL;
ALTER TABLE messages_fts ADD COLUMN messages_fts TEXT;
