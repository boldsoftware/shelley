# Shelley Hooks

You can customize Shelley behavior by placing executable scripts
in `$HOME/.config/shelley/hooks/<name>`.

If a hook fails (non-zero exit, invalid output, etc.) the operation it
belongs to is aborted. The `end-of-turn` hook is the exception: by the
time it fires there is no operation left to abort, so failures are just
logged. A failed `status-bar` hook shows a button in its row that reveals the error.

## Available Hooks

| Hook | Stdin | Stdout |
|---|---|---|
| `system-prompt` | system prompt text | replacement prompt (non-empty) |
| `new-conversation` | JSON | JSON (mutable fields) |
| `chat-message` | JSON | JSON (`message` field) |
| `end-of-turn` | JSON | ignored |
| `status-bar` | JSON | one JSON object with a string or array `body` |

## Example payloads

The samples below are stdin captured from a real Shelley instance with
`tee`-only hooks installed.

### `system-prompt`

Stdin is the rendered prompt text. The first lines look like:

```
You are a subagent of Shelley, a coding agent. You have been delegated
a specific task by the parent agent.

Key constraints:
- Complete your assigned task thoroughly
- Your final message will be returned to the parent agent as the result
...

Working directory: /home/user/project

Git repository root: /home/user/project

<skills>
...
</skills>
```

Stdout must be the (possibly modified) replacement prompt text. A
non-empty result is required.

### `new-conversation`

```json
{
  "prompt": "hello shelley",
  "model": "predictable",
  "cwd": "",
  "readonly": {
    "conversation_id": "cH3UICU",
    "is_subagent": false,
    "headers": [
      ["Accept", "*/*"],
      ["Content-Length", "49"],
      ["Content-Type", "application/json"],
      ["User-Agent", "curl/8.5.0"],
      ["X-Custom", "demo"],
      ["X-Exedev-Email", "test@example.com"]
    ]
  }
}
```

Stdout (all fields optional; empty stdout = no-op):

```json
{ "prompt": "", "model": "", "cwd": "", "slug": "" }
```

For subagent conversations, `readonly.is_subagent` is `true`,
`readonly.parent_id` is set, and `readonly.headers` is absent.

### `chat-message`

```json
{
  "message": "follow-up question",
  "readonly": {
    "conversation_id": "cH3UICU",
    "model": "predictable",
    "reasoning_level": "high",
    "queued": false,
    "headers": [
      ["Accept", "*/*"],
      ["Content-Length", "54"],
      ["Content-Type", "application/json"],
      ["User-Agent", "curl/8.5.0"],
      ["X-Exedev-Email", "test@example.com"]
    ]
  }
}
```

`reasoning_level` is the conversation override when set; otherwise Shelley materializes the service's configured default. It is empty only when the provider chooses the default dynamically and Shelley cannot know it in advance.

Stdout (empty = no-op):

```json
{ "message": "the rewritten message" }
```

### `end-of-turn`

```json
{
  "type": "end_of_turn",
  "conversation_id": "cMT7MTV",
  "timestamp": "2026-05-27T00:34:31.961478145Z",
  "hostname": "vm.exe.xyz",
  "model": "predictable",
  "conversation_url": "https://vm.exe.xyz/",
  "vm_name": "vm",
  "final_response": "Done."
}
```

Stdout is ignored.

### `status-bar`

Adds a row above the conversation's status bar (above the composer on
phones), e.g. with the git branch. It runs in the conversation's
directory, for each browser tab showing the conversation:

- when the conversation opens;
- after each new message: yours, the agent's, each tool result;
- when the model changes;
- when you come back to the tab or its window.

It doesn't run for a tab in the background, a draft, an archived
conversation, or a conversation no tab shows. One run at a time per
tab: triggers during a run make one more run after it, and switching
conversations cancels the run. The row keeps the last output until the
next run finishes. A conversation whose directory is gone shows nothing.

It often runs alongside the agent's tools, so Shelley sets
`GIT_OPTIONAL_LOCKS=0`; a `git status` in the hook then can't make the
agent's `git add` fail on `index.lock`. Times out after 2s, so keep it
cheap: `git branch --show-current`, not a `git status` of a big repo.

```json
{
  "conversation_id": "cMT7MTV",
  "slug": "fix-login",
  "cwd": "/home/exedev/myapp",
  "model": "predictable"
}
```

Stdout is always one JSON object; bare top-level arrays are not accepted.
Its root fields are `body`, `bgcolor`, and `color`. The body is one line of
plain text (URLs are linkified) or a flat array of item objects. Omitted,
null, empty, or whitespace-only body text, an empty item array, and empty
stdout show nothing. The API normalizes visible content to `{ "items": [...] }`;
root `bgcolor` and `color` style the whole row. A null field is the same as
an omitted one. Items with an empty body (or null items) are skipped.

Each array item may contain `body` (plain text), `bgcolor`, `color`, `title`,
`action`, and `args`. Item colors override the row style. `title` supplies the
item's tooltip. An item without an action is plain text; `args` is forbidden
without an action. Root-level item fields such as `title`, `action`, and
`args`, as well as old `href` and flat `commit` fields, are not supported.

Actions are `open_models`, `open_diff`, `open_terminal`, `open_files`,
`open_git`, `open_mcp`, `open_directory`, `open_commands`, and `open_url`.
`open_url` requires `args.url`, an absolute HTTP(S) URL with a hostname. It is
rendered as a real link that opens in a new tab, with `noopener noreferrer`;
localhost links retain Shelley's local-link rewriting.

`open_diff` optionally accepts `args.commit`, a full or abbreviated commit
hash (4–64 hexadecimal characters), not a branch or revision expression.
Omitted `args` or `args: {}` opens the viewer's default selection, just like
the normal Diffs control; this is not necessarily the working diff.
`open_url` accepts only its required `url` argument. Other native actions
accept no argument keys; omitted `args` or
`args: {}` is equivalent. Unknown actions/fields/arguments, invalid types,
or an invalid URL fail the hook and show the error in the row.

```json
{ "body": [{ "body": "Diff 4cd8f2e", "action": "open_diff", "args": { "commit": "4cd8f2e" } }] }
```

A failure (non-zero exit, invalid JSON, unknown fields or actions, over 4 KiB,
a background process left holding its output) shows the error instead.

`open_models` is the current conversation's model/reasoning picker, not
model management. The hook already receives the current `model` and can
use it as the item label. Changing the conversation model refreshes the hook.

```json
{ "body": "main", "bgcolor": "#204a87", "color": "white" }
```

```json
{
  "bgcolor": "var(--bg-secondary)",
  "color": "var(--text-secondary)",
  "body": [
    { "body": "main" },
    { "body": "12 changed files", "action": "open_diff" },
    { "body": "Sonnet", "action": "open_models" },
    { "body": "Proposal", "action": "open_url", "args": { "url": "https://example.com/proposal" } }
  ]
}
```
