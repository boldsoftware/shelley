---
name: shelley-hooks
description: Use when the user wants to customize Shelley by injecting behavior at lifecycle events. It documents Shelley's hooks.
---

Executable files at `~/.config/shelley/hooks/<name>`. Missing or non-executable files are ignored. 30s timeout. Any hook failure (non-zero exit, invalid output, etc.) aborts the operation it belongs to — except `end-of-turn`, where the operation is already finished, so failures are just logged.

Auth-bearing headers (`Cookie`, `Set-Cookie`, `Authorization`, `Proxy-Authorization`) are stripped from the `headers` fields before hooks see them.

## `system-prompt`

Runs on every system prompt (main, subagent, orchestrator, orchestrator-subagent).

- stdin: prompt text
- stdout: replacement prompt text (non-empty)

## `new-conversation`

Runs once when a conversation is created: user-initiated or the first run of a new subagent.

stdin JSON:
```json
{
  "prompt": "...", "model": "...", "cwd": "...",
  "readonly": {
    "conversation_id": "cXXXXXX",
    "is_subagent": false, "parent_id": "...",
    "is_orchestrator": false,
    "headers": [["X-Exedev-Email", "user@example.com"]]
  }
}
```
`parent_id` is `omitempty`. `headers` is a sorted list of `[name, value]` pairs (multi-valued headers produce multiple pairs); omitted for subagent and other non-HTTP entry points.

stdout: same top-level shape. Only `prompt`/`model`/`cwd`/`slug` are read; empty fields mean no change; `readonly` is ignored. Empty stdout = no-op.

Applied when non-empty and changed:
- `cwd` → conversation's working directory
- `model` → re-resolves LLM service; falls back to original if unsupported
- `prompt` → first user message (ignored on distillation paths)
- `slug` → sanitized to a slug-safe form; falls back to async slug on collision

## `chat-message`

Fires when the user posts a follow-up chat message to an existing conversation (`POST /api/conversation/<id>/chat`). For the first message of a brand-new conversation, use `new-conversation`. Not fired for subagent conversations.

stdin JSON:
```json
{
  "message": "the user's chat message",
  "readonly": {
    "conversation_id": "cXXXXXX",
    "model": "claude-sonnet-4.5",
    "reasoning_level": "high",
    "queued": false,
    "headers": [["X-Exedev-Email", "user@example.com"]]
  }
}
```
`reasoning_level` is the conversation's explicit reasoning level (`off`, `minimal`, `low`, `medium`, `high`, or `xhigh`), or the service's configured default when no override is set. It is empty only when the provider chooses the default dynamically and Shelley cannot know it in advance. `queued` is true when the message will be queued (client requested queue mode or the agent is distilling) rather than interrupting the current turn.

stdout: `{"message": "..."}`. Empty stdout, empty `message`, or an identical `message` means no change.

## `end-of-turn`

Fires when an agent finishes a turn — the same signal that drives end-of-turn
notifications (notification channels, push notifications, conversation-hook
webhooks). Suppressed for subagent conversations. Stdout is ignored.

stdin JSON:
```json
{
  "type": "end_of_turn",
  "conversation_id": "cXXXXXX",
  "timestamp": "2024-01-02T03:04:05Z",
  "hostname": "host.exe.xyz",
  "model": "claude-sonnet-4.5",
  "slug": "my-slug",
  "conversation_url": "https://host.exe.xyz/c/my-slug",
  "vm_name": "host",
  "final_response": "agent's last text or tool-call summary"
}
```

Typical uses: play a sound, post a desktop notification, ping a local script.

## `slash/<command>`

Pluggable slash commands. When a user sends a message that starts with
`/<command>` (where `<command>` matches `[a-zA-Z0-9_][a-zA-Z0-9_-]*`), Shelley
looks for an executable at `~/.config/shelley/hooks/slash/<command>`. If
present, it is run synchronously before the message is recorded or sent to
the LLM. Its stdout replaces the user-message body. Empty stdout leaves the
original message unchanged. Applies to both new conversations and follow-up
messages.

No matching executable → the message is treated as a normal user message
(no special handling).

stdin JSON:
```json
{
  "command": "foo",
  "args": "the rest of the message after /foo",
  "raw_message": "/foo the rest of the message after /foo",
  "conversation_id": "cXXXXXX",
  "is_new_conversation": false,
  "cwd": "/home/user/project",
  "model": "claude-sonnet-4.5",
  "user_email": "you@example.com",
  "is_orchestrator": false
}
```

The same context is also exposed via environment variables for shell-friendly
hooks: `SHELLEY_SLASH_COMMAND`, `SHELLEY_SLASH_ARGS`,
`SHELLEY_CONVERSATION_ID`, `SHELLEY_CWD`, `SHELLEY_MODEL`,
`SHELLEY_USER_EMAIL`.

stdout: replacement user-message text. Empty stdout keeps the original
message (useful for hooks that only have side effects). Failure (non-zero
exit) surfaces as a 400 to the client and the message is not recorded.

Example: a `~/.config/shelley/hooks/slash/files` hook that injects the
contents of files matched by a glob:
```sh
#!/bin/sh
set -e
printf 'Here are the files you asked about:\n\n'
for f in $SHELLEY_SLASH_ARGS; do
  printf '=== %s ===\n' "$f"
  cat "$f"
  printf '\n'
done
```
Then `/files src/main.go src/util.go please summarize` becomes a normal user
message with file contents inlined.
