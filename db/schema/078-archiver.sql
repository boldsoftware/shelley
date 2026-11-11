-- The archiver (server/archiver.go): what this Shelley pushes to an archive
-- store, and what the store acked.

-- A conversation created while the archiver flag is on
-- (a non-empty string) is recorded here, with what the store has acked of it.
-- Only recorded conversations are ever pushed, so nothing from before push
-- was turned on is. No foreign key: the row of a deleted conversation is how
-- the deletion reaches the store.
CREATE TABLE archiver_pushes (
    conversation_id TEXT PRIMARY KEY,
    -- The top-level conversation, whose options decide (temporary, kind).
    root_id TEXT NOT NULL,
    -- The highest message sequence_id the store acked.
    pushed_seq INTEGER NOT NULL DEFAULT 0,
    -- The metadata JSON the store last acked; '' if it has none.
    pushed_meta TEXT NOT NULL DEFAULT ''
);

-- The projects JSON the store last acked.
CREATE TABLE archiver_projects (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    projects TEXT NOT NULL
);

-- In the creating transaction, so every creation path records, and a
-- subagent is recorded if its parent is. Temporary and helper (kind)
-- conversations aren't; the push checks again, since a draft's options are
-- set when it is sent. Imported conversations are unrecorded by the import.
CREATE TRIGGER archiver_record AFTER INSERT ON conversations
-- Options that aren't JSON ('' in old rows and tests) are no options.
WHEN json_extract(iif(json_valid(NEW.conversation_options), NEW.conversation_options, '{}'), '$.temporary') IS NOT 1
  AND json_extract(iif(json_valid(NEW.conversation_options), NEW.conversation_options, '{}'), '$.kind') IS NULL
  AND EXISTS (
    SELECT 1 FROM feature_flags
    WHERE name = 'archiver' AND json_valid(value)
      AND json_type(value) = 'text' AND value ->> '$' <> ''
  )
  AND (NEW.parent_conversation_id IS NULL OR EXISTS (
    SELECT 1 FROM archiver_pushes WHERE conversation_id = NEW.parent_conversation_id
  ))
BEGIN
    INSERT INTO archiver_pushes (conversation_id, root_id)
    VALUES (NEW.conversation_id, COALESCE(
        (SELECT root_id FROM archiver_pushes WHERE conversation_id = NEW.parent_conversation_id),
        NEW.conversation_id));
END;
