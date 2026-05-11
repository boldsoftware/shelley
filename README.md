# Shelley: a coding agent for exe.dev

Shelley is a mobile-friendly, web-based, multi-conversation, multi-modal,
multi-model, single-user coding agent built for but not exclusive to
[exe.dev](https://exe.dev/). It does not come with authorization or sandboxing:
bring your own.

*Mobile-friendly* because ideas can come any time.

*Web-based*, because terminal-based scroll back is punishment for shoplifting in some countries.

*Multi-modal* because screenshots, charts, and graphs are necessary, not to mention delightful.

*Multi-model* to benefit from all the innovation going on.

*Single-user* because it makes sense to bring the agent to the compute.

# Installation

## Pre-Built Binaries (macOS/Linux)

```bash
curl -Lo shelley "https://github.com/boldsoftware/shelley/releases/latest/download/shelley_$(uname -s | tr '[:upper:]' '[:lower:]')_$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')" && chmod +x shelley
```

The binaries are on the [releases page](https://github.com/boldsoftware/shelley/releases/latest).

## Homebrew (macOS)

```bash
brew install --cask boldsoftware/tap/shelley
```

## Build from Source

You'll need Go and Node.

```bash
git clone https://github.com/boldsoftware/shelley.git
cd shelley
make
```

# Releases

New releases are automatically created on every commit to `main`. Versions
follow the pattern `v0.N.9OCTAL` where N is the total commit count and 9OCTAL is the commit SHA encoded as octal (prefixed with 9).

# Architecture 

The technical stack is Go for the backend, SQLite for storage, and Typescript
and React for the UI. 

The data model is that Conversations have Messages, which might be from the
user, the model, the tools, or the harness. All of that is stored in the
database, and we use a SSE endpoint to keep the UI updated. 

# History

Shelley is partially based on our previous coding agent effort, [Sketch](https://github.com/boldsoftware/sketch). 

Unsurprisingly, much of Shelley is written by Shelley, Sketch, Claude Code, and Codex. 

# Shelley's Name

Shelley is so named because the main tool it uses is the shell, and I like
putting "-ey" at the end of words. It is also named after Percy Bysshe Shelley,
with an appropriately ironic nod at
"[Ozymandias](https://www.poetryfoundation.org/poems/46565/ozymandias)."
Shelley is a computer program, and, it's an it.

# Open source

Shelley is Apache licensed. We require a CLA for contributions.

# Using Shelley

## Searching conversation history

You can search your conversation history directly using the `!` command to run SQLite queries against Shelley's database.

### List recent conversations

```bash
!sqlite3 "$SHELLEY_DB" "SELECT conversation_id, slug, datetime(created_at, 'localtime') as created, datetime(updated_at, 'localtime') as updated FROM conversations ORDER BY updated_at DESC LIMIT 20;"
```

### Get messages from a specific conversation

Replace `CONVERSATION_ID` with the actual conversation ID from the list above:

```bash
!sqlite3 "$SHELLEY_DB" "SELECT CASE type WHEN 'user' THEN 'User' ELSE 'Agent' END, substr(json_extract(llm_data, '$.Content[0].Text'), 1, 500) FROM messages WHERE conversation_id='CONVERSATION_ID' AND type IN ('user', 'agent') AND json_extract(llm_data, '$.Content[0].Type') = 2 AND json_extract(llm_data, '$.Content[0].Text') != '' ORDER BY sequence_id;"
```

### Search conversations by keyword

```bash
!sqlite3 "$SHELLEY_DB" "SELECT conversation_id, slug FROM conversations WHERE slug LIKE '%SEARCH_TERM%';"
```

The database location is `$SHELLEY_DB` if set, or `$HOME/.config/shelley/shelley.db` by default.

# Building Shelley

Run `make`. Run `make serve` to start Shelley locally.

## Dev Tricks

If you want to see how mobile looks, and you're on your home
network where you've got mDNS working fine, you can
run 

```
socat TCP-LISTEN:9001,fork TCP:localhost:9000
```

