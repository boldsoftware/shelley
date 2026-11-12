# Compaction selection replay

`TestHistoricalCompactionReplay` validates historical inputs against the actual
compaction code without calling a model or writing to the source database.
Normal tests skip it unless all three variables below are set.

```sh
SHELLEY_COMPACTION_REPLAY_DB=/tmp/snapshot.db \
SHELLEY_COMPACTION_REPLAY_CALLS=/tmp/calls.json \
SHELLEY_COMPACTION_REPLAY_OUT=/tmp/results.json \
go test ./server -run '^TestHistoricalCompactionReplay$' -count=1 -v
```

Supply a consistent SQLite backup and a JSON array of original tool calls:

```json
[{
  "conv": "cEXAMPLE", "seq": 123, "generation": 1,
  "date": "2026-11-12 00:00:00", "model": "model-id",
  "call": {
    "ToolName": "compact_in_place",
    "ToolInput": {
      "action": "compact",
      "collapse": [{"from": "2", "to": "4", "note": "Finished setup."}]
    }
  }
}]
```

Sequence is the tool call's message sequence. De-duplicate original calls by
ToolUseID, excluding fork copies; include the correct generation and model.
Keep snapshots and private transcripts outside the checkout. The output file
contains statuses, counts, validation errors, and conversation/sequence IDs,
not source messages, notes, or replacement indexes.

For a before/after comparison, copy this test file into an isolated baseline
worktree and run both versions with the same snapshot and calls manifest.
Compare matching conversation/sequence entries; report accepted-to-rejected
regressions separately. Do not change the inputs or synthesize missing notes.
The test asserts that accepted ranges and their notes are unchanged and that
validation/application do not mutate source history. A validated record that
cannot be applied fails the test.

Denominators matter: explicit index calls are counted separately; missing-note
worker-format calls are unsupported; history reconstruction failures are not
validation rejections. Report all of them. The replay uses production contextItems, including distillation overrides and
provenance/sequence wrappers, with today's token heuristics and the snapshot's
exclusion flags and conversation options—not an exact historical model request. It exercises stored-history reconstruction and selection validation,
not fresh model selection, recovery retries, summarization quality, or latency.
Those behaviors require a separate model-driven experiment.

Tool exchanges with intervening messages form one indivisible indexed span,
named `first..last`. Old IDs inside a newly grouped span are stale: do not
remap them or reuse an old note on expanded coverage to improve replay counts.
Check grouping on the historical source and use fresh selections and notes
when measuring the effect of the new index.
The ~20k per-range target is advisory; larger ranges are not rejected for size.

The index and blocked-range errors include deterministic auto-split plans:
compact contiguous rows with both start/end flags, and keep blocked or recent
rows out of collapse. Plans never expand the requested interval or reuse its
note on smaller pieces. The existing agent writes fresh notes for the chosen
pieces; there is no separate model call to decide the split. Validation stays
atomic until those notes are submitted. Kept outputs can only be explicitly
trimmed when the index marks them `trim`.

`TestCompactSelectionValidation` covers the live server path with the
predictable model. Unknown IDs return ordinary validation errors without a
replacement index. Duplicate, already-trimmed, and output-free trims reject,
as upstream does. Invalid requests persist no partial work.
