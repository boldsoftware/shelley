-- A message's channel id (messages.external_message_id), as archivers push
-- it: a text the source's owner sent, rather than typed in Shelley. NULL
-- for other messages, and from archivers that predate it.
ALTER TABLE archive_store_messages ADD COLUMN external_message_id TEXT;
