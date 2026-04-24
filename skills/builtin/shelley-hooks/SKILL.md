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

Runs once when a conversation is created: user-initiated, distillation (`handleDistillConversation`, `handleDistillReplace`), or the first run of a new subagent.

stdin JSON:
```json
{
  "prompt": "...", "model": "...", "cwd": "...",
  "readonly": {
    "conversation_id": "cXXXXXX",
    "is_subagent": false, "parent_id": "...",
    "is_distillation": false, "source_id": "...",
    "is_orchestrator": false
  }
}
```
`parent_id` and `source_id` are `omitempty`. Distillation leaves `prompt` unset.

stdout: same top-level shape. Only `prompt`/`model`/`cwd` are read; empty fields mean no change; `readonly` is ignored. Empty stdout = no-op.

Applied when non-empty and changed:
- `cwd` → conversation's working directory
- `model` → re-resolves LLM service; falls back to original if unsupported
- `prompt` → first user message (ignored on distillation paths)

Failure (non-zero exit, invalid JSON, etc.) is logged and non-fatal; original values are used, so a broken hook doesn't permanently block conversation creation.
