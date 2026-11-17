---
name: previous-conversations
description: Use when the user references a previous conversation, asks you to continue earlier work, or you need to look up what was discussed before. Includes subagent conversations.
---

Shelley stores top-level and subagent conversation history in a SQLite database.

First, locate the database:

```bash
DB="${SHELLEY_DB:-$HOME/.config/shelley/shelley.db}"
```

`$SHELLEY_CONVERSATION_ID` is the conversation you are in now. The list and search queries below exclude it; it is not a previous conversation.

## List recent conversations

```bash
sqlite3 "$DB" "SELECT conversation_id, slug, parent_conversation_id, json_extract(conversation_options, '$.kind') AS kind, datetime(created_at, 'localtime') as created, datetime(updated_at, 'localtime') as updated FROM conversations WHERE conversation_id != '$SHELLEY_CONVERSATION_ID' ORDER BY updated_at DESC LIMIT 20;"
```

## Get messages from a conversation

Replace CONVERSATION_ID with the actual ID:

```bash
sqlite3 "$DB" "SELECT CASE m.type WHEN 'user' THEN 'User' ELSE 'Agent' END, substr(json_extract(content.value, '$.Text'), 1, 500) FROM messages AS m, json_each(m.llm_data, '$.Content') AS content WHERE m.conversation_id='CONVERSATION_ID' AND m.type IN ('user', 'agent') AND json_extract(content.value, '$.Type') = 2 AND trim(COALESCE(json_extract(content.value, '$.Text'), '')) != '' ORDER BY m.sequence_id, CAST(content.key AS INTEGER);"
```

## Get a conversation's subagents

A subagent ID, such as one shown in a `transcribed by subagent` marker, is itself a CONVERSATION_ID. Use it directly with the messages query above.

To discover child IDs from a parent, replace PARENT_CONVERSATION_ID below. Shelley links child conversations—including ordinary subagents and specialized workers—through `parent_conversation_id`; `kind` and `archived` help distinguish them.

```bash
sqlite3 "$DB" "SELECT conversation_id, slug, json_extract(conversation_options, '$.kind') AS kind, archived, datetime(created_at, 'localtime') as created, datetime(updated_at, 'localtime') as updated FROM conversations WHERE parent_conversation_id='PARENT_CONVERSATION_ID' ORDER BY created_at;"
```

Use a returned `conversation_id` with the messages query above. Repeat the child query with that ID to follow nested subagents.

## Search conversations by slug

```bash
sqlite3 "$DB" "SELECT conversation_id, slug FROM conversations WHERE conversation_id != '$SHELLEY_CONVERSATION_ID' AND slug LIKE '%SEARCH_TERM%';"
```
