-- The Shelley archive store, its archiver, its research index and temporary
-- messages are gone. Drop what migrations 077-082 made, where they ran: the
-- triggers, the views, the archiver's and temporary messages' bookkeeping
-- (ids, cursors and a copy of the projects) and the flags' overrides. The store's
-- tables (archive_store_*, archive_research_*) go only where empty
-- (migrationSteps in db.go); ones with data stay, inert, with their indexes
-- archive_store_conversations_updated and archive_research_docs_conversation:
-- a new migration must not reuse these names.
DROP TRIGGER IF EXISTS archiver_record;
DROP TRIGGER IF EXISTS temporary_message;
DROP TRIGGER IF EXISTS archive_store_messages_fts_ai;
DROP TRIGGER IF EXISTS archive_store_messages_fts_ad;
DROP TRIGGER IF EXISTS archive_research_docs_ai;
DROP TRIGGER IF EXISTS archive_research_docs_ad;
DROP TRIGGER IF EXISTS archive_research_message_ai;
DROP TRIGGER IF EXISTS archive_research_message_ad;
DROP TRIGGER IF EXISTS archive_research_conversation_ai;
DROP TRIGGER IF EXISTS archive_research_conversation_au;
DROP TRIGGER IF EXISTS archive_research_conversation_ad;
DROP TRIGGER IF EXISTS archive_research_projects_au;
DROP VIEW IF EXISTS archive_research_message_docs;
DROP VIEW IF EXISTS archive_research_title_docs;
DROP VIEW IF EXISTS archive_research_instruction_docs;
DROP TABLE IF EXISTS archiver_pushes;
DROP TABLE IF EXISTS archiver_projects;
DROP TABLE IF EXISTS temporary_messages;
DROP TABLE IF EXISTS temporary_runs;
DELETE FROM feature_flags WHERE name IN ('archive-store', 'archiver', 'archive-research');
