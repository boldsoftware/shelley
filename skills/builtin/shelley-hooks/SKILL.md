---
name: shelley-hooks
description: Use when the user wants to customize Shelley by injecting behavior at lifecycle events. It documents Shelley's hooks.
---

Executable files at `~/.config/shelley/hooks/<name>`. Missing or non-executable files are ignored. 30s timeout. Any hook failure (non-zero exit, invalid output, etc.) aborts the operation it belongs to — except `end-of-turn`, where the operation is already finished, so failures are just logged, and `status-bar`, which shows a button in its row that reveals the error.

Auth-bearing headers (`Cookie`, `Set-Cookie`, `Authorization`, `Proxy-Authorization`) are stripped from the `headers` fields before hooks see them.

## `system-prompt`

Runs on every system prompt (main, subagent).

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
    "headers": [["X-Exedev-Email", "user@example.com"]]
  }
}
```
`parent_id` is `omitempty`. `headers` is a sorted list of `[name, value]` pairs (multi-valued headers produce multiple pairs); omitted for subagent and other non-HTTP entry points.

stdout: same top-level shape. Only `prompt`/`model`/`cwd`/`slug` are read; empty fields mean no change; `readonly` is ignored. Empty stdout = no-op.

Applied when non-empty and changed:
- `cwd` → conversation's working directory
- `model` → re-resolves LLM service; falls back to original if unsupported
- `prompt` → first user message
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

## `status-bar`

Adds a row above the conversation's status bar (above the composer on phones),
e.g. with the git branch. Runs in the conversation's directory, for each browser
tab showing the conversation: when it opens, after each new message (each tool
result too), and when you come back to the tab or its window. Never for a
background tab, a draft, an archived conversation, one no tab shows, or if the
directory is gone. One run at a time per tab: triggers during a run make one
more run after it, and switching conversations cancels the run. The row keeps
the last output until the next run finishes. 2s timeout: keep it cheap
(`git branch --show-current`, not `git status` of a big repo). It often runs
alongside the agent's tools; Shelley sets `GIT_OPTIONAL_LOCKS=0` so a
`git status` in it can't make the agent's git commands fail on `index.lock`.

stdin JSON:
```json
{
  "conversation_id": "cXXXXXX",
  "slug": "my-slug",
  "cwd": "/home/user/project",
  "model": "claude-sonnet-4.5"
}
```

stdout: one JSON object; bare top-level arrays are not accepted. The only
root fields are `body`, `bgcolor`, and `color`. `body` is one line of plain
text or a flat array of item objects; URLs in plain text are linkified.
Omitted, null, empty, or whitespace-only body text, an empty item array, and
empty stdout show nothing. The API normalizes visible content to
`{ "items": [...] }`; root `bgcolor` and `color` style the whole row.
A null field is the same as an omitted one. Items with an empty body (or
null items) are skipped.
```json
{ "body": "main", "bgcolor": "#204a87", "color": "white" }
```

Each item may include `body` (plain text), `bgcolor`, `color`, `title`,
`action`, and `args`. Item colors override the row style; `title` supplies an
item tooltip. An item without an action is plain text, and `args` is forbidden
without an action. Root-level item fields, `href`, and flat `commit` are not
supported.

Actions are `open_models`, `open_diff`, `open_terminal`, `open_files`,
`open_git`, `open_mcp`, `open_directory`, `open_commands`, and `open_url`.
`open_url` requires `args.url`, an absolute HTTP(S) URL with a hostname. It
renders as a real link opening in a new tab with `noopener noreferrer`;
localhost links retain Shelley's local-link rewriting.

`open_models` opens the conversation's model/reasoning picker, not model
management. Use the stdin `model` as its label to show the current choice.
Changing the conversation model refreshes the hook.

`open_diff` optionally accepts `args.commit`, a full or abbreviated commit
hash (4–64 hexadecimal characters), not a branch or revision expression.
Omitted `args` or `args: {}` opens the viewer's default selection, just like
the normal Diffs control; this is not necessarily the working diff.
`open_url` accepts only its required `url` argument. Other native actions
accept no argument keys; omitted `args` or
`args: {}` is equivalent. Unknown actions/fields/arguments, invalid types,
or an invalid URL fail and show the error in the row.

An array body can mix plain text, links, and panel actions:
```json
{
  "bgcolor": "var(--bg-secondary)",
  "color": "var(--text-secondary)",
  "body": [
    { "body": "main" },
    { "body": "docs", "action": "open_url", "args": { "url": "https://example.com/docs" } },
    { "body": "Models", "action": "open_models" }
  ]
}
```

Example: open a diff for a commit:
```json
{
  "body": [
    { "body": "Diff 4cd8f2e", "action": "open_diff", "args": { "commit": "4cd8f2e" } }
  ]
}
```

Failures (non-zero exit, invalid JSON, unknown fields or actions, over 4 KiB,
a background process left holding its output) show as an error in the row.

Example: show the branch, red on `main`:
```sh
#!/bin/sh
branch=$(git branch --show-current 2>/dev/null)
[ -n "$branch" ] || exit 0
if [ "$branch" = main ]; then
  printf '{"body":"%s","bgcolor":"firebrick","color":"white"}\n' "$branch"
else
  printf '{"body":"%s"}\n' "$branch"
fi
```

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
  "user_email": "you@example.com"
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
