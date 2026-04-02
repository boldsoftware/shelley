# Plan: Hashline-Anchored Edit Tool

## Problem

The current `patch` tool's `replace` operation requires the LLM to reproduce `oldText` byte-for-byte. LLMs are bad at this — they drop comments, normalize whitespace, skip blank lines. When `replace` fails, the agent falls back to `overwrite`, which silently destroys comments/context the LLM didn't reproduce. See `replace-problems.md` for a detailed incident report.

## Approach: Steal from oh-my-pi's hashline system

oh-my-pi solved this elegantly. Instead of text matching, every line gets a short hash tag when displayed (`LINE#HASH:content`). Edits reference lines by `LINE#HASH` — the line number addresses it, the hash validates it hasn't changed since the model last read the file. No more reproducing old text.

### How it works in oh-my-pi

1. **Reading**: When a file is shown to the model, each line is prefixed: `42#VP:  return nil`
   - `42` = 1-indexed line number
   - `VP` = 2-char hash (xxHash32 of normalized line, encoded via a custom 16-char alphabet `ZPMQVRWSNKTXJBYH`)
2. **Editing**: The model says "replace range `5#VP` to `12#QM` with these new lines"
3. **Validation**: Before mutating, all referenced anchors are checked. If any hash mismatches (file changed since read), the error shows the current correct hashes so the model can retry.
4. **Operations**: `replace_line`, `replace_range`, `append_at`, `prepend_at`, `append_file`, `prepend_file`

## What we build (Go, for Shelley)

### Phase 1: Core hashline library (`claudetool/hashline/`)

- `hashline.go`: Core functions
  - `ComputeLineHash(idx int, line string) string` — xxHash32-based 2-char hash using the same `ZPMQVRWSNKTXJBYH` alphabet as oh-my-pi (for ecosystem compatibility)
  - `FormatLineTag(line int, text string) string` — returns `"42#VP"`
  - `FormatHashLines(text string, startLine int) string` — annotates full file text
  - `ParseTag(ref string) (Anchor, error)` — parses `"42#VP"` back to struct
  - `ValidateLineRef(ref Anchor, fileLines []string) error` — checks hash matches
  - For lines with no alphanumeric chars (only punctuation/whitespace like `}`, `{`, blank), mix in the line number as seed to reduce collisions (oh-my-pi does this)
- `edit.go`: Edit types and application
  - Edit types: `ReplaceLine`, `ReplaceRange`, `AppendAt`, `PrependAt`, `AppendFile`, `PrependFile`
  - `ApplyEdits(text string, edits []Edit) (string, error)` — validates all hashes first, then applies bottom-up (highest line first so splices don't shift earlier line numbers)
  - `HashlineMismatchError` — rich error showing context around mismatched lines with correct hashes, so model can self-correct
- `hashline_test.go`, `edit_test.go`: Tests

### Phase 2: Wire into patch tool (`claudetool/patch.go`)

Option A (new tool): Add a separate `edit` tool alongside `patch`.
Option B (extend patch): Add new operations to the existing `patch` tool schema.

**I recommend Option A** — a new `edit` tool. Reasons:
- The patch tool's `replace` operation with `oldText` matching is fundamentally different from hashline-anchored edits. Mixing them in one tool creates confusion.
- We can experiment and iterate on `edit` without breaking `patch`.
- Eventually `edit` replaces `patch` entirely.

The new `edit` tool:
- **Input schema**: `{ path, edits: [{ loc, content }] }` where `loc` is one of:
  - `{ range: { pos: "N#ID", end: "N#ID" } }` — replace inclusive range
  - `{ append: "N#ID" }` — insert after line
  - `{ prepend: "N#ID" }` — insert before line
  - `"append"` — append to file
  - `"prepend"` — prepend to file
- `content` is `[]string` (array of lines) or `null` (delete)

### Phase 3: Hashline-annotated file reading

The model needs to see hashline-annotated output when it reads files. Two approaches:

**Option A**: Modify the bash tool's `cat` output to include hashline prefixes.
**Option B**: Add a dedicated `read` tool that returns hashline-annotated content.

**I recommend Option B** — a `read` tool. The bash tool already has line-number formatting for cat output; a dedicated `read` tool can return hashline-prefixed lines cleanly. The model uses `read` before `edit`, establishing the read→edit workflow.

The `read` tool:
- Input: `{ path, startLine?, endLine? }` (optional range for large files)
- Output: hashline-formatted content, e.g.:
  ```
  1#VP:package main
  2#QM:
  3#SN:import "fmt"
  ```

### Phase 4: System prompt / tool description

Critical: the tool description needs to teach the model the workflow clearly. Steal oh-my-pi's prompt structure — it's well-designed with examples showing:
- How to read anchors from `read` output
- How to construct range replacements
- The boundary gotcha (closing `}` duplication)
- Single-line replacement (pos == end)
- Deletion (content: null)

### Phase 5: Auto-corrections and guardrails (from oh-my-pi)

- **Escaped tab autocorrection**: If model writes `\t` instead of real tabs, auto-fix
- **Boundary duplication warning**: If last replacement line matches next surviving line, warn
- **Noop detection**: If a replace results in identical content, report it

## What we DON'T build (yet)

- Streaming hashline formatter (oh-my-pi has this for large files; we can add later)
- Import management (oh-my-pi's `imports` field for auto-managing imports)
- Compact diff preview (oh-my-pi's post-edit summary; our existing diff display is fine)

## Decisions

1. **New `edit` tool** alongside `patch`. Patch stays for new files and overwrite.
2. **New `read` tool**. Encourage via prompting, don't modify bash cat.
3. **Go stdlib hashing** (crc32, 3-char base-62 = 238K values). No cross-tool compat needed.
4. **Keep both** tools available.
5. **No overwrite in `edit`** — leave that in `patch`/`bash`.
