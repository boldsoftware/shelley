# Shelley HTTP/SSE API

This document describes the API contract between a Shelley server and its
clients (the web UI, the iOS app, the CLI in `client/`, and tests). All
routes are mounted under `/api/` unless noted; the version endpoint is at
`/version`.

## Capabilities

```
GET /version
```

Returns build info plus `capabilities` (string list). Capabilities
advertise optional, additive features that clients can opt into when
present; a client that doesn't recognize a capability just doesn't use
it, and an older server that doesn't ship the field is equivalent to
advertising none.

Capabilities advertise optional behavior. Current capabilities include
`thinking-levels`, `drafts`, and `queued-transcriptions`; clients that do not
recognize one ignore it.

## Stream architecture

The server derives the conversation list from the database and publishes
RFC-6902 JSON Patch diffs on a single unified SSE stream:

- `GET /api/stream2` — unified SSE: per-conversation messages **and**
  conversation-list patches.
- `GET /api/conversations/snapshot` — seed the patch stream with the
  current list and its content hash.
- Previews are embedded in each conversation list row (`preview`,
  `preview_updated_at`).
- `GET /api/conversation/<id>/stream` survives for non-web clients (iOS,
  CLI, tests) but new clients should use `/api/stream2`.

The patch stream is driven exclusively by `Pool.OnCommit`: every
successful write transaction triggers a recompute, and a `recomputeMu`
serializes recomputes so concurrent commits can't publish events out of
order. Each event carries `old_hash`/`new_hash`, letting clients
reconcile their state on every patch.

## Endpoints

### Versioning

- `GET /version` — `{tag, commit, commit_time, capabilities: [...]}`.
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
  first user message. The request accepts `message`, optional `model` and
  `cwd`, and optional `conversation_options`:
  ```json
  {
    "message": "run the tests",
    "model": "glm-5.3-fireworks",
    "conversation_options": {
      "thinking_level": "high",
      "tool_overrides": {"bash": "off"},
      "disable_all_tools": false,
      "disable_notifications": true
    }
  }
  ```
  `thinking_level` is one of `off`, `minimal`, `low`, `medium`, `high`,
  `xhigh`, or `max`; the server also validates it against the selected model's
  advertised levels. Tool override values are `on` or `off`. With
  `disable_all_tools`, an explicit `on` override re-enables that tool.
  The conversation starts from the settings of the profile named by
  `conversation_options.profile`, or the default profile (see Profiles
  below). A non-empty `model` overrides the profile's, as do the
  `thinking_level`, `tool_overrides`, `compact_nudge_tokens`, and
  `system_prompt` options where present, even as `""`, `{}`, or `0`.
- `POST /api/conversations/distill-new-generation` — compact the current
  conversation into the next generation of the same conversation. The
  optional `method` field (`default` or `compact`) is accepted for
  compatibility but is ignored: compaction is always used.

`ConversationWithState` row shape:

| field | meaning |
|---|---|
| `conversation_id`, `slug`, `created_at`, `updated_at`, `cwd`, `archived`, `parent_conversation_id`, `model`, `conversation_options`, `current_generation`, `agent_working`, `user_initiated` | DB columns |
| `working` | mirror of `agent_working`, kept for the patch-stream contract |
| `git_repo_root`, `git_worktree_root`, `git_commit`, `git_subject` | optional, from a cached HEAD lookup keyed by `cwd` |
| `subagent_count` | number of subagent conversations whose `parent_conversation_id` matches this row |
| `preview`, `preview_updated_at` | trailing text of the most recent agent message and its timestamp (RFC 3339); empty if no agent reply yet, or if this conversation is outside the 500-most-recent window the server tracks for previews |

### Single conversation

- `GET /api/conversation/<id>` — full API message history (compressed) plus
  conversation metadata. Message rows include `message_id`,
  `conversation_id`, `sequence_id`, `type`, `llm_data`, `user_data`,
  `usage_data`, `other_usage_data`, `created_at`, `display_data`, `generation`,
  `end_of_turn`, `llm_api_url`, `model_name`, `forked_from_message_id`, and
  `user_email`. The JSON-valued database columns (`llm_data`, `user_data`,
  `usage_data`, `other_usage_data`, and `display_data`) are encoded as JSON
  strings on the wire. `llm_data.Content` carries text, thinking, tool-use,
  and tool-result blocks. Provider continuation secrets and inline image data
  are removed before serving.
- `GET /api/conversation/<id>/stream` — **legacy** SSE: messages, state,
  no list patches. Used by iOS, CLI, and Go tests; new clients should
  use `/api/stream2`. Query params:
    - `?last_sequence_id=<n>` — resume from `sequence_id > n`.
    - `?tail=<n>` — first frame contains only the last `n` messages.
  A `{"snapshot_complete": true}` frame follows the initial replay
  and precedes live updates.
- `POST /api/conversation/<id>/chat` — send a user message. A message of
  `/transcription <absolute uploaded-media path>` creates a durable specialized
  queued message and returns `202 {"status":"queued"}`. The path must name a
  regular file inside `/tmp/shelley-uploads`. A hidden low-reasoning child
  performs the transcription independently of the HTTP request. While it is
  working or failed, the item reserves its FIFO position; when ready, the
  ordinary queue drainer creates the immutable parent user turn. Optional
  context follows the path on subsequent lines of the command and is prepended
  to the transcript, preserving text and ready attachments that were present
  before recording. Video results include the original recording and generated
  contact sheet paths in that turn.
- `POST /api/conversation/<id>/send-queued?queued_id=<id>` — interrupt the
  running turn and immediately drain the FIFO head. The supplied id must still
  name that head item.
- `POST /api/conversation/<id>/cancel-queued?queued_id=<id>` — cancel and remove
  one queued item. Omitting `queued_id` cancels the entire queue. Cancelling a
  working transcription also stops its hidden child.
- `POST /api/conversation/<id>/retry-queued?queued_id=<id>` — retry a failed
  queued transcription in place.
- `POST /api/conversation/<id>/cancel` — interrupt the running loop.
- `POST /api/conversation/<id>/archive` / `unarchive`.
- `POST /api/conversation/<id>/hooks` — register an end-of-turn webhook.
- `POST /api/conversation/<id>/tags` — replace the conversation's tag list.
- `GET /api/conversation/<id>/settings` — the settings later turns use, and
  the profile they came from (a label; either may have changed since):
  ```json
  {"profile": "Research", "model": "claude-opus-5.5", "thinking_level": "high",
   "tool_overrides": {"browser": "off"}, "compact_nudge_tokens": 200000,
   "system_prompt": ""}
  ```
- `POST /api/conversation/<id>/settings` — change them. The body has the same
  fields; those left out keep their values, and `profile`, if present, first
  replaces all of them with that profile's. `"model": ""` means the server's
  default. When `thinking_level` is left out, a model that lacks the level
  rounds it to one it has; an explicit level the model lacks is a `400`, as
  are unknown fields. Responds with the settings now in effect. Unless only
  the profile label changes, a running turn is stopped first, and queued
  messages then go out on the new settings (those the turn had already taken
  in end with it, as with Stop); `409` if another turn starts before the
  change lands.
  Changes record a `modelchange` message saying what changed (`profile_to`,
  `from`/`to`, `reasoning_to`, `tools_on`/`tools_off`, `compact_nudge_tokens`,
  `system_prompt_changed`, `text`, and the settings before, `previous`), just
  after a new `system` message if the template or the tools changed. A fork
  from before the change gets the settings before it. `409` if the
  conversation is archived or a draft (a draft's settings travel with its
  first message), for a custom `system_prompt` in a subagent, or for anything
  but the model and reasoning in a btw reader.
- `GET /api/conversation/<id>/subagents` — direct child conversations. Clients
  can recurse through this endpoint when they need the complete descendant
  tree (for example, aggregate usage reporting).
- `GET /api/conversation-by-slug/<slug>` — lookup by slug.

### Unified stream

```
GET /api/stream2?conversation=<id>&conversation_list_hash=<h>&last_sequence_id=<n>
```

SSE stream. A single connection delivers per-conversation events
(messages, tool progress, stream deltas, conversation/state updates) for
**all** active conversations on the server, plus the conversation-list
patch stream. Every per-conversation event carries a top-level
`conversation_id` field for client-side routing.

All query params are optional:

- `conversation` — if set, the first frames replay that conversation's
  message history before live updates begin. It governs **backfill
  only**: live events for every conversation flow regardless.
- `conversation_list_hash` — the `hash` from the most recent snapshot
  or patch event the client successfully applied. The server uses it to
  decide whether to replay history or send a fresh reset event.
- `last_sequence_id`, `tail` — refine the `conversation` backfill, with
  the same semantics as on `/api/conversation/<id>/stream`. A
  `snapshot_complete` frame separates the initial replay (and an empty
  replay on connections without `conversation`) from live updates.

Event payload (`data: <json>`):

```ts
interface StreamResponse {
  // Routing key for per-conversation events. Always set on messages,
  // conversation, conversation_state, context_window_size, tool_progress,
  // and stream_delta. Empty for connection-scoped frames
  // (conversation_list_patch, heartbeat, snapshot_complete) and for
  // global events that already carry their own conversation reference
  // (notification_event).
  conversation_id?: string;

  // Per-conversation event payload. With a single stream serving every
  // active conversation, clients dispatch based on conversation_id.
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
  snapshot_complete?: true;         // once, after the initial replay
}
```

The `conversation_list_patch` operates on a document that is exactly the
`conversations` array returned by `/api/conversations/snapshot`. Clients
should:

1. `GET /api/conversations/snapshot` once to obtain `(state, hash)`.
2. Open `/api/stream2?conversation_list_hash=<hash>`.
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

- `GET /api/find-files?dir=&q=&content=skip&include_dirs=true` — fuzzy name
  search. `dir` is absolute; `q` is optional. `include_dirs` is an optional
  boolean (default false); enabling it includes nested and empty folders in
  name results. Invalid or repeated `include_dirs` values return 400.
  Results contain `search_dir` and `matches`; paths are relative to
  `search_dir`. Folder matches have `is_dir: true` and a trailing `/` in
  `path`; file matches omit `is_dir`. `content=only` always returns file-content
  hits, never folders. An explicit existing directory path without a trailing
  slash offers that folder itself when `include_dirs` is true; a trailing
  slash browses inside it. File-only callers retain their existing behavior.
- `GET /api/list-directory?path=` — directory listing.
- `POST /api/create-directory` — `mkdir -p`.
- `POST /api/write-file` — write a file.
- `POST /api/upload` — binary upload (multipart).
- `POST /api/upload/raw?filename=` — binary upload with the file content as
  the request body (no multipart framing). Newer clients prefer this to
  avoid building a multipart body on device. Older servers return 404/405;
  clients should fall back to the multipart endpoint.
- `GET /api/upload/raw` — empty `200 OK` if the server supports the raw
  upload endpoint; clients use this as a capability probe (older servers
  return 404/405).

- `GET /api/read?path=` — read a file (images served as `image/*`).
- `POST /api/validate-cwd` — check whether a path is a valid working
  directory.
- `GET /api/user-agents-md` / `POST` — read/write the per-user
  AGENTS.md.

### Models, tools, notifications

- `GET /api/models` — available models.
- `GET /api/tools` — registered tool definitions.
- `GET/POST/PUT/DELETE /api/custom-models[/<id>]` — custom model CRUD.
  OpenAI-compatible models accept `reasoning_replay` as `auto`,
  `reasoning_content`, or `none`; responses include
  `resolved_reasoning_replay` when catalog resolution is known.
- `POST /api/custom-models-test` — test a custom model config.
- `GET/POST/PUT/DELETE /api/notification-channels[/<id>]`,
  `GET /api/notification-channel-types` — notification CRUD.

### Profiles

A profile is a named set of conversation settings: `model` (`""` means the
server's default model), `thinking_level` (`""` means the model's default),
`tool_overrides`, `compact_nudge_tokens` (`0` means the default), and
`system_prompt`, a Go [text/template](https://pkg.go.dev/text/template)
(`""` means Shelley's built-in prompt, which changes with Shelley). Exactly
one profile is the default; new conversations start from it unless they name
another. A conversation records the profile it came from in
`conversation_options.profile`, and keeps its settings when the profile
changes later.

- `GET /api/profiles` — all profiles, default first:
  `[{"name", "default", "model", "thinking_level", "tool_overrides",
  "compact_nudge_tokens", "system_prompt"}]`.
- `POST /api/profiles` — create one: `name` (trimmed; up to 64 bytes, no
  `/` or control characters, not `.` or `..`) and the settings fields (unknown ones are a `400`); `409` if
  the name is taken.
- `PUT /api/profiles/<name>` — replace a profile's settings; the body has
  only the settings fields. Profiles can't be renamed.
- `POST /api/profiles/<name>/default` — make it the default profile.
- `DELETE /api/profiles/<name>` — `409` for the default profile.
- `GET /api/system-prompt` — the built-in template and the variables a
  template can use: `{"template", "variables": [{"name", "description"}]}`.
  Templates are rendered with `missingkey=error` and checked on save, with
  every optional variable both set and unset (outside a git repository,
  `.GitInfo` is nil); a template that fails is a `400` saying where and why,
  e.g. `Invalid system_prompt: line 2: unknown variable .Nope`.
- `POST /api/system-prompt/check` — check a template as a save would:
  `{"template"}` → `{"error": null}` or
  `{"error": {"line", "column", "message"}}`, the line and the column (in
  characters) counted from 1 and left out when unknown.

### Shell

- `WS /api/exec-ws?cwd=` — websocket for an interactive shell session.
  `cmd=` starts a new persistent session; `term_id=` reattaches to an existing
  one and never reruns the command. `conversation_id=` records which
  conversation owns a newly spawned terminal.
- `GET /api/terminals` — all persistent terminals, unfiltered.
  `conversation_id` is the owning conversation, or `null` for a terminal shown
  in every conversation.
- `PUT /api/terminals/<id>/scope` — move a terminal between conversations.
  Body `{"conversation_id": "<id>"}` confines it to that conversation;
  `{"conversation_id": null}` shows it in all of them. `null` is the only
  accepted spelling of global: an absent field or an empty string is a 400.
  Returns the updated terminal.
- `DELETE /api/terminals/<id>`, `POST /api/terminals/<id>/kill` — terminate a
  terminal and drop its record.

### Debug

- `GET /debug/` — index of the debug pages.
- `GET /debug/conversation-stream/history` — JSON dump of the last 100
  patch events.
