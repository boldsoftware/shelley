import {
  aggregateOtherUsage,
  buildCostSummary,
  buildOtherUsageBreakdown,
  buildTokenCostStack,
  callXLayout,
  formatDuration,
  formatTokenCount,
  formatUsd,
  generationStarts,
  OtherUsageRow,
  segmentColor,
  timeXLayout,
  TOKEN_BANDS,
  UsageEntry,
  yTicks,
} from "./tokenCostGraph";

let passed = 0;
let failed = 0;
function assert(cond: boolean, msg: string) {
  if (cond) passed++;
  else {
    failed++;
    console.error(`FAIL: ${msg}`);
  }
}
function approx(a: number, b: number) {
  return Math.abs(a - b) < 1e-9;
}

const opusCost = { input: 5, output: 25, cache_read: 0.5, cache_write: 6.25 };

function entry(partial: Partial<UsageEntry>): UsageEntry {
  return {
    input_tokens: 0,
    cache_creation_input_tokens: 0,
    cache_read_input_tokens: 0,
    output_tokens: 0,
    cost_usd: 0,
    model: "claude-opus-4-6",
    ...partial,
  };
}

// Weighted stacking: two calls, cumulative dollars.
{
  const entries = [
    entry({ input_tokens: 1_000_000, output_tokens: 100_000 }),
    entry({ cache_read_input_tokens: 2_000_000, output_tokens: 200_000 }),
  ];
  const s = buildTokenCostStack(entries, { "claude-opus-4-6": opusCost });
  assert(s.weighted, "stack is weighted when pricing known");
  assert(s.n === 2, "two points");
  // Call 1: input $5, output $2.5. Call 2 adds: cacheRead $1, output $5.
  // Bands bottom-to-top: cacheRead, cacheWrite, input, output.
  assert(approx(s.layers[0][0], 0), "cacheRead layer at call 1");
  assert(approx(s.layers[2][0], 5), "input boundary at call 1");
  assert(approx(s.layers[3][0], 7.5), "top at call 1");
  assert(approx(s.layers[0][1], 1), "cacheRead boundary at call 2");
  assert(approx(s.layers[3][1], 13.5), "top at call 2 (cumulative)");
  assert(approx(s.maxY, 13.5), "maxY = final top");
  assert(s.perModel.length === 1 && s.perModel[0].model === "claude-opus-4-6", "one model");
  assert(s.perModel[0].priced, "model priced");
  const outRow = s.perModel[0].rows.find((t) => t.band.key === "output_tokens")!;
  assert(outRow.tokens === 300_000, "output token total");
  assert(approx(outRow.cost, 7.5), "output cost total");
  assert(outRow.unitUsdPerMtok === 25, "output unit price");
  assert(approx(s.perModel[0].totalCost, 13.5), "model total cost");
}

// Unweighted fallback: no pricing for any model -> raw token counts.
{
  const entries = [entry({ model: "mystery", input_tokens: 100, output_tokens: 50 })];
  const s = buildTokenCostStack(entries, { mystery: null });
  assert(!s.weighted, "unweighted when nothing priced");
  assert(approx(s.maxY, 150), "maxY is raw tokens");
  assert(s.perModel.length === 1 && !s.perModel[0].priced, "unpriced model recorded");
}

// Mixed: priced + unpriced. Unpriced contributes 0 dollars but is reported.
// Every (model, band) segment gets its own color.
{
  const entries = [
    entry({ input_tokens: 1_000_000 }),
    entry({ model: "mystery", input_tokens: 500_000 }),
  ];
  const s = buildTokenCostStack(entries, { "claude-opus-4-6": opusCost, mystery: null });
  assert(s.weighted, "weighted when at least one model priced");
  assert(approx(s.maxY, 5), "unpriced tokens add $0");
  assert(s.perModel.length === 2, "two models broken out");
  assert(s.segments.length === 2 * TOKEN_BANDS.length, "one segment per (model, band)");
  assert(s.layers.length === s.segments.length, "one layer per segment");
  const colors = new Set(s.segments.map((seg) => seg.color));
  assert(colors.size === s.segments.length, "all segment colors distinct");
  const rowColors = new Set(s.perModel.flatMap((m) => m.rows.map((r) => r.color)));
  assert(rowColors.size === s.segments.length, "legend row colors match segments");
  const mystery = s.perModel.find((m) => m.model === "mystery")!;
  assert(!mystery.priced, "mystery unpriced");
  assert(
    mystery.rows.find((t) => t.band.key === "input_tokens")!.tokens === 500_000,
    "mystery input tokens tracked",
  );
}

// Other-usage aggregation: raw parsed entries → per-(purpose, model, url) rows.
{
  const rows = aggregateOtherUsage([
    { purpose: "compaction", model: "opus", input_tokens: 100, output_tokens: 50, cost_usd: 0.01 },
    { purpose: "compaction", model: "opus", input_tokens: 30, cache_read_input_tokens: 7 },
    { purpose: "compaction", model: "haiku", input_tokens: 1 },
    { purpose: "slug", model: "opus", url: "https://x", output_tokens: 5, cost_usd: 0.02 },
  ]);
  assert(rows.length === 3, "otherAgg: grouped by (purpose, model, url)");
  const comp = rows[0];
  assert(comp.purpose === "compaction" && comp.model === "opus", "otherAgg: first-seen order");
  assert(comp.llm_calls === 2, "otherAgg: llm_calls counts entries");
  assert(comp.input_tokens === 130, "otherAgg: input tokens summed");
  assert(comp.cache_read_input_tokens === 7, "otherAgg: missing fields treated as 0");
  assert(comp.output_tokens === 50, "otherAgg: output tokens summed");
  assert(approx(comp.cost_usd, 0.01), "otherAgg: reported cost summed");
  assert(rows[1].model === "haiku" && rows[1].llm_calls === 1, "otherAgg: model splits rows");
  assert(rows[2].purpose === "slug" && rows[2].url === "https://x", "otherAgg: url preserved");
  assert(aggregateOtherUsage([]).length === 0, "otherAgg: empty input");
}

// Other-usage breakdown: per-purpose aggregation with estimated cost.
{
  const row = (partial: Partial<OtherUsageRow>): OtherUsageRow => ({
    purpose: "compaction",
    model: "claude-opus-4-6",
    llm_calls: 1,
    input_tokens: 0,
    cache_creation_input_tokens: 0,
    cache_read_input_tokens: 0,
    output_tokens: 0,
    cost_usd: 0,
    ...partial,
  });

  const b = buildOtherUsageBreakdown(
    [
      row({ purpose: "compaction", llm_calls: 2, input_tokens: 1_000_000, output_tokens: 100_000 }),
      row({ purpose: "slug", llm_calls: 3, cache_read_input_tokens: 2_000_000, cost_usd: 0.25 }),
      // Second model for the same purpose merges into one per-purpose row.
      row({ purpose: "slug", model: "mystery", llm_calls: 4, input_tokens: 500, cost_usd: 0.1 }),
    ],
    { "claude-opus-4-6": opusCost, mystery: null },
  );
  assert(b.perPurpose.length === 2, "other: purposes merged across models");
  const compaction = b.perPurpose[0];
  assert(compaction.purpose === "compaction", "other: first-seen purpose order");
  assert(compaction.llmCalls === 2, "other: compaction calls");
  assert(compaction.tokens === 1_100_000, "other: compaction tokens across bands");
  // input $5 + output $2.5
  assert(approx(compaction.estimatedUsd, 7.5), "other: compaction estimate");
  assert(compaction.priced, "other: compaction fully priced");
  const slug = b.perPurpose[1];
  assert(slug.llmCalls === 7, "other: slug calls sum across models");
  assert(slug.tokens === 2_000_500, "other: slug tokens sum across models");
  // Only the priced model contributes: cacheRead $1.
  assert(approx(slug.estimatedUsd, 1), "other: unpriced model adds $0");
  assert(!slug.priced, "other: purpose with an unpriced model flagged");
  assert(approx(slug.reportedUsd, 0.35), "other: per-purpose reported cost sums");
  assert(
    approx(slug.reportedUnpricedUsd, 0.1),
    "other: reported cost from the unpriced model is isolated",
  );
  assert(compaction.reportedUsd === 0, "other: compaction has no reported cost");
  assert(approx(b.totals.estimatedUsd, 8.5), "other: total estimate");
  assert(approx(b.totals.reportedUsd, 0.35), "other: reported cost sums");
  assert(
    approx(b.totals.reportedUnpricedUsd, 0.1),
    "other: unpriced reported cost sums without priced calls",
  );
  assert(b.totals.llmCalls === 9, "other: total calls");
  assert(b.totals.unpricedCalls === 4, "other: unpriced calls counted");

  const empty = buildOtherUsageBreakdown([], {});
  assert(empty.perPurpose.length === 0 && empty.totals.llmCalls === 0, "other: empty rows");
}

// Cost summary: one obvious total that includes this conversation, indirect
// usage, and subagents. The values mirror real conversations from the local
// Shelley database.
{
  const minikomi = buildCostSummary(132.619, {
    other: { estimatedUsd: 1.895, reportedUnpricedUsd: 0, unpricedCalls: 0 },
    subagents: { estimatedUsd: 55.161, reportedUnpricedUsd: 0, unpricedCalls: 0 },
  });
  assert(approx(minikomi.totalUsd, 189.675), "summary: total includes every bucket");
  assert(approx(minikomi.otherUsd, 1.895), "summary: other bucket retained");
  assert(approx(minikomi.subagentUsd, 55.161), "summary: subagent bucket retained");
  assert(minikomi.unpricedCalls === 0, "summary: no unpriced calls");

  const mixedKnownCosts = buildCostSummary(21.051, {
    other: { estimatedUsd: 1.5, reportedUnpricedUsd: 0.25, unpricedCalls: 2 },
    subagents: { estimatedUsd: 968.721, reportedUnpricedUsd: 0.75, unpricedCalls: 3 },
  });
  assert(
    approx(mixedKnownCosts.totalUsd, 992.272),
    "summary: estimates and unpriced reported costs both contribute",
  );
  assert(mixedKnownCosts.unpricedCalls === 5, "summary: unpriced calls sum across buckets");
  assert(
    mixedKnownCosts.conversationUnpricedCalls === 0 &&
      mixedKnownCosts.otherUnpricedCalls === 2 &&
      mixedKnownCosts.subagentUnpricedCalls === 3,
    "summary: unpriced calls remain attributed to their buckets",
  );

  const pricedSubagentsOnly = buildCostSummary(0, {
    conversationUnpricedCalls: 4,
    subagents: { estimatedUsd: 12.5, reportedUnpricedUsd: 0, unpricedCalls: 0 },
  });
  assert(
    approx(pricedSubagentsOnly.totalUsd, 12.5),
    "summary: priced subagents survive unpriced direct usage",
  );
  assert(
    pricedSubagentsOnly.conversationUnpricedCalls === 4,
    "summary: direct unpriced calls retained",
  );
}

// Reported cost accumulates, both overall and per model.
{
  const s = buildTokenCostStack([entry({ cost_usd: 0.5 }), entry({ cost_usd: 0.25 })], {
    "claude-opus-4-6": opusCost,
  });
  assert(approx(s.reportedCostUsd, 0.75), "reported cost sums");
  assert(approx(s.perModel[0].reportedUsd, 0.75), "per-model reported cost sums");
}

// Empty input.
{
  const s = buildTokenCostStack([], {});
  assert(s.n === 0 && s.maxY === 0, "empty stack");
}

// Generation delineators.
{
  const gens = [1, 1, 2, 2, 2, 3].map((generation) => entry({ generation, input_tokens: 1 }));
  const starts = generationStarts(gens);
  assert(starts.length === 2 && starts[0] === 2 && starts[1] === 5, "generation starts");
  assert(generationStarts([entry({ input_tokens: 1 })]).length === 0, "single entry no starts");
  assert(generationStarts([]).length === 0, "empty no starts");
}

// Call-number x layout: evenly spaced, one turn.
{
  const lay = callXLayout(3);
  assert(
    lay.xs.length === 3 && approx(lay.xs[0], 0) && approx(lay.xs[1], 0.5) && approx(lay.xs[2], 1),
    "call layout evenly spaced",
  );
  assert(
    lay.turns.length === 1 && lay.turns[0][0] === 0 && lay.turns[0][1] === 2,
    "call layout single turn",
  );
  assert(callXLayout(0).xs.length === 0, "call layout empty");
  assert(callXLayout(1).xs.length === 1, "call layout single point");
}

// Time x layout: x advances with wall-clock time within turns; turn breaks
// insert a fixed gap regardless of idle duration.
{
  const t0 = 1_000_000;
  const entries = [
    entry({ input_tokens: 1, timestamp: t0, startsTurn: true }),
    entry({ input_tokens: 1, timestamp: t0 + 10_000 }),
    entry({ input_tokens: 1, timestamp: t0 + 20_000 }),
    // 1 hour idle, then a new turn of 10s.
    entry({ input_tokens: 1, timestamp: t0 + 3_620_000, startsTurn: true }),
    entry({ input_tokens: 1, timestamp: t0 + 3_630_000 }),
  ];
  const lay = timeXLayout(entries);
  assert(lay.turns.length === 2, "two turns");
  assert(lay.turns[0][0] === 0 && lay.turns[0][1] === 2, "first turn span");
  assert(lay.turns[1][0] === 3 && lay.turns[1][1] === 4, "second turn span");
  assert(lay.activeMs === 30_000, "active time excludes idle between turns");
  assert(approx(lay.xs[0], 0), "starts at 0");
  assert(approx(lay.xs[4], 1), "ends at 1");
  // Idle hour is a fixed gap: turn 1 covers 20s of 30s active time.
  const timeFrac = 1 - 0.025;
  assert(approx(lay.xs[2], (20_000 / 30_000) * timeFrac), "within-turn position ∝ time");
  assert(approx(lay.xs[3] - lay.xs[2], 0.025 + 0), "turn gap is fixed width");
  // Equal spacing fallback when timestamps are missing.
  const flat = timeXLayout([
    entry({ input_tokens: 1, startsTurn: true }),
    entry({ input_tokens: 1 }),
    entry({ input_tokens: 1 }),
  ]);
  assert(approx(flat.xs[1], 0.5), "degenerate timestamps space evenly");
  // A multi-call turn whose calls share one timestamp (second-granularity
  // clocks) collapses to zero width; its xs are equal so the component can
  // draw it as a slab.
  const zw = timeXLayout([
    entry({ input_tokens: 1, timestamp: 1000, startsTurn: true }),
    entry({ input_tokens: 1, timestamp: 1000 }),
    entry({ input_tokens: 1, timestamp: 61_000, startsTurn: true }),
    entry({ input_tokens: 1, timestamp: 71_000 }),
  ]);
  assert(zw.turns.length === 2 && approx(zw.xs[0], zw.xs[1]), "zero-width turn has equal xs");
  assert(timeXLayout([]).xs.length === 0, "time layout empty");
  assert(timeXLayout([entry({ input_tokens: 1 })]).turns.length === 1, "time layout single point");

  // turnStartTimestamp anchors the first call of a turn: created_at marks
  // call completion, so the trigger-to-first-completion span counts as
  // active time and gives the first call horizontal extent.
  const anchored = timeXLayout([
    entry({ input_tokens: 1, timestamp: t0 + 5_000, startsTurn: true, turnStartTimestamp: t0 }),
    entry({ input_tokens: 1, timestamp: t0 + 15_000 }),
  ]);
  assert(anchored.activeMs === 15_000, "anchor adds first-call duration");
  assert(approx(anchored.xs[0], 5_000 / 15_000), "first call positioned after lead-in");
  // Anchor clamps to the previous call's completion: a queued message (sent
  // while the agent was still working) must not overlap the previous turn.
  const queued = timeXLayout([
    entry({ input_tokens: 1, timestamp: t0 + 10_000, startsTurn: true, turnStartTimestamp: t0 }),
    entry({
      input_tokens: 1,
      timestamp: t0 + 20_000,
      startsTurn: true,
      turnStartTimestamp: t0 + 5_000, // queued before previous call finished
    }),
  ]);
  assert(queued.activeMs === 10_000 + 10_000, "queued anchor clamped to previous call");
  // Anchor after the call itself (clock skew) contributes nothing negative.
  const skew = timeXLayout([
    entry({ input_tokens: 1, timestamp: t0, startsTurn: true, turnStartTimestamp: t0 + 9_999 }),
    entry({ input_tokens: 1, timestamp: t0 + 10_000 }),
  ]);
  assert(skew.activeMs === 10_000, "future anchor clamps to zero lead-in");
}

// Y-axis ticks: round values strictly inside (0, maxY).
{
  const t = yTicks(108);
  assert(t.length > 0 && t.every((v) => v > 0 && v < 108), "ticks inside range");
  assert(
    t.every((v) => approx(v % 25, 0)),
    "round dollar steps for ~$108",
  );
  assert(yTicks(0).length === 0, "no ticks for empty");
  const small = yTicks(0.9);
  assert(small.length > 0 && small[0] === 0.2, "sub-dollar ticks");
}

// Segment colors: distinct across models and bands, valid HSL. Palette
// supports 6 models before cycling.
{
  const seen = new Set<string>();
  for (let m = 0; m < 6; m++)
    for (let b = 0; b < TOKEN_BANDS.length; b++) seen.add(segmentColor(b, m));
  assert(seen.size === 24, "24 distinct colors for 6 models × 4 bands");
  assert(/^hsl\(\d+ \d+% \d+%\)$/.test(segmentColor(0, 0)), "hsl format");
  assert(segmentColor(0, 6) === segmentColor(0, 0), "7th model cycles");
}

// Duration formatting.
assert(formatDuration(42_000) === "42s", "formatDuration seconds");
assert(formatDuration(720_000) === "12m", "formatDuration minutes");
assert(formatDuration(11_100_000) === "3h 05m", "formatDuration hours");

// Formatting.
assert(formatUsd(0) === "$0", "formatUsd 0");
assert(formatUsd(0.0042) === "$0.0042", "formatUsd small");
assert(formatUsd(0.5) === "$0.500", "formatUsd sub-dollar");
assert(formatUsd(12.345) === "$12.35", "formatUsd dollars");
assert(formatTokenCount(999) === "999", "tokens raw");
assert(formatTokenCount(12_000) === "12k", "tokens k");
assert(formatTokenCount(999_600) === "1.0M", "tokens rounding at 1M boundary");
assert(formatTokenCount(3_400_000) === "3.4M", "tokens M");
assert(TOKEN_BANDS.length === 4, "four bands");

console.log(`tokenCostGraph: ${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
