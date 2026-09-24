import assert from "node:assert/strict";
import type { Usage } from "../generated-types";
import {
  aggregateOtherUsage,
  buildOtherUsageBreakdown,
  buildRequestUsageEntries,
  buildTokenCostStack,
  countConfirmedUnpricedCalls,
  requestBreakdown,
  timeXLayout,
} from "./tokenCostGraph";

const rates = { input: 5, output: 25, cache_read: 0.5, cache_write: 6.25 };
const start = "2026-06-01T12:00:00Z";
const firstEnd = "2026-06-01T12:00:01Z";
const secondStart = "2026-06-01T12:00:02Z";
const end = "2026-06-01T12:00:04Z";
const context = {
  snippet: "A paused turn",
  generation: 2,
  timestamp: Date.parse("2026-06-01T12:00:08Z"),
  startsTurn: true,
};
const usage: Usage = {
  model: "model",
  url: "https://provider.example/v1/messages",
  input_tokens: 3_000_000,
  cache_creation_input_tokens: 1_000_000,
  cache_read_input_tokens: 3_000_000,
  output_tokens: 300_000,
  cost_usd: 0.75,
  start_time: start,
  end_time: end,
  requests: [
    {
      input_tokens: 1_000_000,
      cache_creation_input_tokens: 0,
      cache_read_input_tokens: 2_000_000,
      output_tokens: 100_000,
      cost_usd: 0.25,
      start_time: start,
      end_time: firstEnd,
    },
    {
      input_tokens: 2_000_000,
      cache_creation_input_tokens: 1_000_000,
      cache_read_input_tokens: 1_000_000,
      output_tokens: 200_000,
      cost_usd: 0.5,
      start_time: secondStart,
      end_time: end,
    },
  ],
};
const before = JSON.stringify(usage);
const entries = buildRequestUsageEntries(usage, context);
assert.equal(entries.length, 2);
assert.deepEqual(
  entries.map((e) => [e.start_time, e.end_time, e.timestamp, e.cost_usd]),
  [
    [start, firstEnd, Date.parse(firstEnd), 0.25],
    [secondStart, end, Date.parse(end), 0.5],
  ],
);
assert.deepEqual(
  entries.map((e) => [e.startsTurn, e.turnStartTimestamp]),
  [
    [true, Date.parse(start)],
    [false, undefined],
  ],
);
for (const e of entries) {
  assert.equal(e.model, usage.model);
  assert.equal(e.url, usage.url);
  assert.equal(e.snippet, context.snippet);
  assert.equal(e.generation, context.generation);
  assert.equal("requests" in e, false);
}
assert.equal(timeXLayout(entries).activeMs, 4_000);
const stack = buildTokenCostStack(entries, { model: rates });
assert.equal(stack.n, 2);
assert.equal(stack.maxY, 30.25);
assert.equal(stack.reportedCostUsd, 0.75);
assert.equal(stack.perModel[0].reportedUsd, 0.75);
assert.equal(countConfirmedUnpricedCalls(entries, { model: null }), 2);
assert.equal(countConfirmedUnpricedCalls(entries, {}), 0);
assert.equal(buildTokenCostStack(entries, {}).maxY, 7_300_000);

// Preserve a human-message anchor when present; subsequent requests do not
// start new turns or acquire their own anchor.
const humanStart = Date.parse(start) - 1_000;
const anchored = buildRequestUsageEntries(usage, {
  ...context,
  turnStartTimestamp: humanStart,
});
assert.equal(timeXLayout(anchored).activeMs, 5_000);
assert.equal(anchored[1].turnStartTimestamp, undefined);
assert.deepEqual(
  buildRequestUsageEntries(usage, { ...context, startsTurn: false }).map((e) => e.startsTurn),
  [false, false],
);

// Old single records (including empty/null request lists) still count once.
for (const requests of [undefined, null, []]) {
  const legacy = { ...usage, requests, start_time: undefined, end_time: undefined };
  assert.equal(requestBreakdown(legacy).length, 1);
  const oldEntries = buildRequestUsageEntries(legacy, context);
  assert.equal(oldEntries.length, 1);
  assert.equal(oldEntries[0].timestamp, context.timestamp);
  assert.equal(oldEntries[0].turnStartTimestamp, undefined);
  assert.equal(buildTokenCostStack(oldEntries, { model: rates }).maxY, 30.25);
  assert.equal(buildTokenCostStack(oldEntries, {}).reportedCostUsd, 0.75);
}

// Missing leg timing must not inherit the whole paused turn's interval.
const noLegTiming = buildRequestUsageEntries(
  { ...usage, requests: [{ ...usage.requests![0], start_time: undefined, end_time: undefined }] },
  context,
);
assert.equal(noLegTiming[0].start_time, undefined);
assert.equal(noLegTiming[0].end_time, undefined);
assert.equal(noLegTiming[0].timestamp, context.timestamp);

const zero: Usage = {
  input_tokens: 0,
  cache_creation_input_tokens: 0,
  cache_read_input_tokens: 0,
  output_tokens: 0,
  cost_usd: 0,
};
assert.equal(buildRequestUsageEntries(zero, context).length, 0, "skip legacy placeholders");
assert.equal(buildRequestUsageEntries({ ...zero, cost_usd: 0.5 }, context).length, 1);
assert.equal(buildRequestUsageEntries({ ...zero, requests: [zero, zero] }, context).length, 2);

// Indirect rows use the same breakdown; grouping preserves purpose/model/URL,
// request counts, and provider costs without also adding aggregate totals.
const rows = aggregateOtherUsage([
  { ...usage, purpose: "llm_one_shot" },
  { ...usage, requests: undefined, purpose: "llm_one_shot" },
  { ...usage, model: "unknown", purpose: "compaction" },
  { ...usage, url: "https://other.example", requests: [], purpose: "llm_one_shot" },
]);
assert.equal(rows.length, 3);
assert.deepEqual(
  rows.map((r) => r.llm_calls),
  [3, 2, 1],
);
assert.deepEqual(
  rows.map((r) => r.cost_usd),
  [1.5, 0.75, 0.75],
);
assert.equal(rows[0].input_tokens, 6_000_000);
assert.equal(rows[0].cache_creation_input_tokens, 2_000_000);
assert.equal(rows[0].cache_read_input_tokens, 6_000_000);
assert.equal(rows[0].output_tokens, 600_000);
assert.equal(rows[2].url, "https://other.example");
const breakdown = buildOtherUsageBreakdown(rows, { model: rates, unknown: null });
assert.equal(breakdown.totals.llmCalls, 6);
assert.equal(breakdown.totals.unpricedCalls, 2);
assert.equal(breakdown.totals.reportedUsd, 3);
assert.equal(breakdown.totals.reportedUnpricedUsd, 0.75);
assert.equal(breakdown.totals.estimatedUsd, 90.75);
assert.equal(breakdown.perPurpose[0].llmCalls, 4);
assert.equal(breakdown.perPurpose[0].tokens, 21_900_000);
assert.equal(breakdown.perPurpose[1].llmCalls, 2);
assert.equal(countConfirmedUnpricedCalls(rows, { model: rates, unknown: null }), 2);
assert.equal(JSON.stringify(usage), before, "usage must not be mutated");

console.log("requestUsage: passed");
