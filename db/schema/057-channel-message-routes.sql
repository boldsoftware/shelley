-- Texted messages delivered to the workspace Assistant rather than to a
-- conversation bound to their chat record the chat and endpoint they came
-- from, so that replies to them go back there.
CREATE TABLE channel_message_routes (
    conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) ON DELETE CASCADE,
    external_message_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (conversation_id, external_message_id)
);
CREATE INDEX channel_message_routes_chat ON channel_message_routes(chat_id, created_at);
