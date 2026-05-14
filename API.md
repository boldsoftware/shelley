# Shelley HTTP/SSE API

This document describes the API contract between a Shelley server and its
clients (the web UI, the iOS app, the CLI in `client/`, and tests). All
routes are mounted under `/api/` unless noted; the version endpoint is at
`/version`.

## Protocol version

```
GET /version
```

Returns build info plus `protocol_version` (integer). Clients should check
this on connect and refuse to talk to a server with a different major
version.

**Current version: `2`.**

### Protocol 1 (historical)

In protocol 1 the conversation list and per-conversation streams were
separate SSE endpoints:

- `GET /api/conversations/stream` — list-level events (`conversation_list_update`,
  `conversation_state_update`, `subagent_update`, etc.) for sidebar UIs.
- `GET /api/conversation/<id>/stream` — per-conversation message stream.
- `GET /api/conversations/previews` — a side-channel `map<id, {text,
  updated_at}>` of last-agent-message previews that the web UI fetched
  once at mount.
- `GET /version` returned only build info; no `protocol_version` field.

The UI had to merge events from multiple streams, dedupe its own
optimistic conversation-list updates, and re-fetch previews on its own
schedule. As traffic grew this became hard to keep coherent: dropped
events could desync the list, and there was no way for a reconnecting
client to verify it was in a consistent state.

### Protocol 2 (current)

The server now derives the conversation list from the database and
publishes RFC-6902 JSON Patch diffs on a single unified SSE stream:

- `GET /api/stream` — unified SSE: per-conversation messages **and**
  conversation-list patches.
- `GET /api/conversations/snapshot` — seed the patch stream with the
  current list and its content hash.
- Previews are now embedded in each conversation list row (`preview`,
  `preview_updated_at`); the `/api/conversations/previews` endpoint is
  gone.
- `GET /api/conversations/stream` is gone.
- `GET /api/conversation/<id>/stream` survives for non-web clients (iOS,
  CLI, tests) but new clients should use `/api/stream`.
- `Modified` was removed from the `/version` build info; clients should
  read `protocol_version` instead.

The patch stream is driven exclusively by `Pool.OnCommit`: every
successful write transaction triggers a recompute, and a `recomputeMu`
serializes recomputes so concurrent commits can't publish events out of
order. Each event carries `old_hash`/`new_hash`, letting clients
reconcile their state on every patch.

## Endpoints

### Versioning

- `GET /version` — `{tag, commit, commit_time, protocol_version: 2}`.
- `GET /version-check` — `{has_update, current_tag, latest_tag, ...}`.
- `GET /version-changelog` — markdown changelog.

### Conversation list

Unless noted, results exclude **archived** conversations.

- `GET /api/conversations?limit=&offset=` — top-level (non-subagent)
  unarchived conversations as plain rows. Used by iOS and the CLI.
- `GET /api/conversations/snapshot` — the current unarchived list
  including subagents, plus per-row state (working, git info, subagent
  count, preview) and the patch-stream hash. Used by the web UI on load.
  Response:
  ```json
  {
    "conversations": [ConversationWithState, ...],
    "hash": "<sha256 hex>"
  }
  ```
- `GET /api/conversations/archived` — archived list.
- `POST /api/conversations/new` — create a conversation and post the
  first user message.
- `POST /api/conversations/distill-new-generation` — distill the current
  conversation into the next generation of the same conversation.

`ConversationWithState` row shape:

| field | meaning |
|---|---|
| `conversation_id`, `slug`, `created_at`, `updated_at`, `cwd`, `archived`, `parent_conversation_id`, `model`, `conversation_options`, `current_generation`, `agent_working`, `user_initiated` | DB columns |
| `working` | mirror of `agent_working`, kept for the patch-stream contract |
| `git_repo_root`, `git_worktree_root`, `git_commit`, `git_subject` | optional, from a cached HEAD lookup keyed by `cwd` |
| `subagent_count` | number of subagent conversations whose `parent_conversation_id` matches this row |
| `preview`, `preview_updated_at` | trailing text of the most recent agent message and its timestamp (RFC 3339); empty if no agent reply yet, or if this conversation is outside the 500-most-recent window the server tracks for previews |

### Single conversation

- `GET /api/conversation/<id>` — full message history (compressed).
- `GET /api/conversation/<id>/stream` — **legacy** SSE: messages, state,
  no list patches. Used by iOS, CLI, and Go tests; new clients should
  use `/api/stream`.
- `POST /api/conversation/<id>/chat` — send a user message.
- `POST /api/conversation/<id>/cancel` — interrupt the running loop.
- `POST /api/conversation/<id>/archive` / `unarchive`.
- `POST /api/conversation/<id>/hooks` — register an end-of-turn webhook.
- `GET /api/conversation-by-slug/<slug>` — lookup by slug.

### Unified stream

```
GET /api/stream?conversation=<id>&conversation_list_hash=<h>&last_sequence_id=<n>
```

SSE stream. All query params are optional:

- `conversation` — if set, the stream also emits messages, tool
  progress, and state for that conversation. If omitted, the stream
  only emits conversation-list patches and heartbeats.
- `conversation_list_hash` — the `hash` from the most recent snapshot
  or patch event the client successfully applied. The server uses it to
  decide whether to replay history or send a fresh reset event.
- `last_sequence_id` — resume per-conversation message delivery from
  this `sequence_id` (the largest the client has already applied).

Event payload (`data: <json>`):

```ts
interface StreamResponse {
  // Per-conversation (only when ?conversation= is set):
  messages?: APIMessage[];
  conversation?: Conversation;
  conversation_state?: { conversation_id, working, model };
  context_window_size?: number;
  tool_progress?: ToolProgress;
  stream_delta?: StreamDelta;
  notification_event?: NotificationEvent;

  // Conversation-list patch stream:
  conversation_list_patch?: {
    old_hash: string | null,        // null on a reset event
    new_hash: string,
    patch: RFC6902Op[],             // ops with paths like "/0", "/0/working", etc.
    at: string,                     // RFC 3339
    reset?: true,                   // true for the seed event
  };

  heartbeat?: true;                 // sent every 30s if nothing else to say
}
```

The `conversation_list_patch` operates on a document that is exactly the
`conversations` array returned by `/api/conversations/snapshot`. Clients
should:

1. `GET /api/conversations/snapshot` once to obtain `(state, hash)`.
2. Open `/api/stream?conversation_list_hash=<hash>`.
3. For each `conversation_list_patch` event:
   - If `event.old_hash == null` or `event.reset`, replace local state
     with `event.patch[0].value`.
   - Otherwise, require `event.old_hash == currentHash`; apply
     `event.patch` via RFC 6902; assert `hashList(state) == new_hash`.
   - On any mismatch, drop local state and resume with the snapshot.

Reconnect semantics: the server keeps the last 100 patch events in
memory. If the client's `conversation_list_hash` matches one of those
boundaries, the server replays the missed patches; otherwise it sends a
fresh reset event.

### Git

- `GET /api/git/repos` — repo discovery.
- `GET /api/git/diffs?cwd=` — staged/unstaged file lists.
- `GET /api/git/diffs/<commit>?cwd=` — committed file lists.
- `GET /api/git/file-diff/<path>?cwd=&base=&head=` — unified diff.
- `GET /api/git/graph?cwd=` — commit graph.
- `GET /api/git/commit-detail?cwd=&sha=` — single commit.
- `GET /api/git/commit-messages?cwd=` — recent commit messages.
- `POST /api/git/amend-message` — amend HEAD message.
- `POST /api/git/create-worktree` — `git worktree add`.

### Files & directories

- `GET /api/list-directory?path=` — directory listing.
- `POST /api/create-directory` — `mkdir -p`.
- `POST /api/write-file` — write a file.
- `POST /api/upload` — binary upload (multipart).
- `POST /api/upload/raw?filename=` — binary upload with the file content as
  the request body (no multipart framing). Newer clients prefer this to
  avoid building a multipart body on device. Older servers return 404/405;
  clients should fall back to the multipart endpoint.
- `GET /api/read?path=` — read a file (images served as `image/*`).
- `POST /api/validate-cwd` — check whether a path is a valid working
  directory.
- `GET /api/user-agents-md` / `POST` — read/write the per-user
  AGENTS.md.

### Models, tools, notifications

- `GET /api/models` — available models.
- `GET /api/tools` — registered tool definitions.
- `GET/POST/PUT/DELETE /api/custom-models[/<id>]` — custom model CRUD.
- `POST /api/custom-models-test` — test a custom model config.
- `GET/POST/PUT/DELETE /api/notification-channels[/<id>]`,
  `GET /api/notification-channel-types` — notification CRUD.

### Shell

- `WS /api/exec-ws?cwd=` — websocket for an interactive shell session.

### Debug

- `GET /debug/conversations` — HTML dump of the conversation list.
- `GET /debug/conversation-stream` — HTML viewer over the patch stream.
- `GET /debug/conversation-stream/history` — JSON dump of the last 100
  patch events.
