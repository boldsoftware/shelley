---
name: shelley-hooks
description: Use when the user wants to customize Shelley by injecting behavior at lifecycle events. It documents Shelley's hooks.
---

Executable files at `~/.config/shelley/hooks/<name>`. Missing or non-executable files are ignored. 30s timeout. Per-hook failure semantics below.

Implementation: `shelley/server/system_prompt.go`.

## `system-prompt`

Runs on every system prompt (main, subagent, orchestrator, orchestrator-subagent).

- stdin: prompt text
- stdout: replacement prompt text (non-empty)
- failure (non-zero exit or empty stdout): returns an error, blocking the operation

## `new-conversation`

Runs once when a conversation is created: user-initiated or the first run of a new subagent.

stdin JSON:
```json
{
  "prompt": "...", "model": "...", "cwd": "...",
  "readonly": {
    "conversation_id": "cXXXXXX",
    "is_subagent": false, "parent_id": "...",
    "is_orchestrator": false
  }
}
```
`parent_id` is `omitempty`.

stdout: same top-level shape. Only `prompt`/`model`/`cwd` are read; empty fields mean no change; `readonly` is ignored. Empty stdout = no-op.

Applied when non-empty and changed:
- `cwd` → conversation's working directory
- `model` → re-resolves LLM service; falls back to original if unsupported
- `prompt` → first user message (ignored on distillation paths)

Failure (non-zero exit, invalid JSON, etc.) is logged and non-fatal; original values are used, so a broken hook doesn't permanently block conversation creation.

## `end-of-turn`

Fires when an agent finishes a turn — the same signal that drives end-of-turn
notifications (notification channels, push notifications, conversation-hook
webhooks). Suppressed for subagent conversations. Runs asynchronously in a
background goroutine; stdout is ignored.

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

Failure (non-zero exit, etc.) is logged and non-fatal. Typical uses: play a
sound, post a desktop notification, ping a local script.
