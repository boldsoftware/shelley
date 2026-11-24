-- The composer's unsent text (draft) is saved for started conversations too,
-- not only for drafts, so it follows the user between clients.
-- draft_updated_at versions that text: clients reconcile their local copy
-- against it, and a send clears the text only if it was composed from that
-- version. It is apart from updated_at, which orders the conversation list:
-- saving a started conversation's text must not reorder it. NULL until the
-- text is first saved.
ALTER TABLE conversations ADD COLUMN draft_updated_at DATETIME;
-- Clients reconciled a draft's text against updated_at until now.
UPDATE conversations SET draft_updated_at = updated_at WHERE is_draft;
