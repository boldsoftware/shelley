import {
  aggregateOtherUsage,
  buildCostSummary,
  buildModelCostComparison,
  buildOtherUsageBreakdown,
  buildTokenCostStack,
  callXLayout,
  countConfirmedUnpricedCalls,
  deltaBars,
  effectiveCost,
  generationBoundary,
  formatDuration,
  formatTokenCount,
  formatUsd,
  generationStarts,
  ModelUsage,
  OtherUsageRow,
  segmentColor,
  stackedAreaPaths,
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

function modelUsage(
  model: string,
  priced: boolean,
  rates: [number, number, number, number],
  tokens: [number, number, number, number],
  reportedUsd = 0,
  colorPrefix = model,
): ModelUsage {
  const rows = TOKEN_BANDS.map((band, i) => ({
    band,
    tokens: tokens[i],
    unitUsdPerMtok: rates[i],
    cost: priced ? (tokens[i] * rates[i]) / 1e6 : 0,
    color: `${colorPrefix}-${i}`,
  }));
  return {
    model,
    priced,
    rows,
    totalCost: rows.reduce((sum, row) => sum + row.cost, 0),
    reportedUsd,
  };
}

// One shared table row aligns the main conversation and sub-agent columns.
{
  const main = modelUsage("shared", true, [1, 2, 3, 4], [1_000_000, 0, 0, 0], 99, "main");
  const sub = modelUsage("shared", true, [1, 2, 3, 4], [2_000_000, 0, 0, 0], 88, "sub");
  const compared = buildModelCostComparison([main], [sub]);
  assert(compared.length === 1 && compared[0].model === "shared", "comparison: shared model row");
  assert(approx(compared[0].main!.knownUsd, 1), "comparison: main known cost");
  assert(approx(compared[0].subagents!.knownUsd, 2), "comparison: sub-agent known cost");
  assert(compared[0].main!.reportedUsd === 0, "comparison: priced report not double-counted");
  assert(compared[0].main!.rows[0].unitUsdPerMtok === 1, "comparison: shared rate retained");
  assert(compared[0].main!.rows[0].color === "main-0", "comparison: first main color retained");
  assert(compared[0].subagents!.rows[0].color === "sub-0", "comparison: first sub color retained");
}

// Main models lead in first-seen order, followed by sub-agent-only models.
{
  const main = [
    modelUsage("main-a", true, [1, 1, 1, 1], [1, 0, 0, 0]),
    modelUsage("main-b", true, [1, 1, 1, 1], [1, 0, 0, 0]),
  ];
  const sub = [
    modelUsage("main-b", true, [1, 1, 1, 1], [1, 0, 0, 0]),
    modelUsage("sub-only", true, [1, 1, 1, 1], [1, 0, 0, 0]),
  ];
  const compared = buildModelCostComparison(main, sub);
  assert(
    compared.map((row) => row.model).join(",") === "main-a,main-b,sub-only",
    "comparison: main order then sub-only",
  );
  assert(
    compared[0].main !== undefined && compared[0].subagents === undefined,
    "comparison: main-only scope",
  );
  assert(
    compared[2].main === undefined && compared[2].subagents !== undefined,
    "comparison: sub-only scope",
  );
}

// Multiple endpoints for one model retain independent priced costs; only an
// unpriced endpoint's provider report is used as fallback.
{
  const pricedA = modelUsage("multi", true, [5, 1, 1, 1], [1_000_000, 0, 0, 0], 50, "a");
  const pricedB = modelUsage("multi", true, [7, 1, 1, 1], [2_000_000, 0, 0, 0], 60, "b");
  const unpriced = modelUsage("multi", false, [0, 0, 0, 0], [3_000_000, 0, 0, 0], 4, "u");
  const column = buildModelCostComparison([], [pricedA, pricedB, unpriced])[0].subagents!;
  assert(approx(column.knownUsd, 23), "comparison: priced costs plus unpriced report once");
  assert(approx(column.reportedUsd, 4), "comparison: only unpriced reports isolated");
  assert(!column.priced, "comparison: partial pricing flagged");
  assert(column.rows[0].tokens === 6_000_000, "comparison: endpoint tokens aggregated");
  assert(approx(column.rows[0].cost, 19), "comparison: known row costs aggregated");
  assert(
    !column.rows[0].priced && column.rows[0].unitUsdPerMtok === null,
    "comparison: partial row pricing has no rate",
  );
  assert(column.rows[0].color === "a-0", "comparison: first endpoint color retained");

  const sameRate = buildModelCostComparison(
    [],
    [pricedA, modelUsage("multi", true, [5, 1, 1, 1], [2, 0, 0, 0])],
  )[0].subagents!;
  assert(sameRate.priced && sameRate.rows[0].priced, "comparison: fully priced endpoints");
  assert(sameRate.rows[0].unitUsdPerMtok === 5, "comparison: identical endpoint rate retained");
}

// An unpriced endpoint with no output must not erase a known output rate.
{
  const priced = modelUsage("partial", true, [1, 2, 3, 4], [1_000_000, 0, 0, 2_000_000]);
  const unpriced = modelUsage("partial", false, [0, 0, 0, 0], [1_000_000, 0, 0, 0]);
  const column = buildModelCostComparison([], [priced, unpriced])[0].subagents!;
  assert(
    !column.priced && !column.rows[0].priced,
    "comparison: unknown read usage remains partial",
  );
  assert(
    column.rows[3].priced && column.rows[3].unitUsdPerMtok === 4,
    "comparison: fully priced output keeps its rate",
  );
  assert(
    approx(column.rows[3].cost, 8),
    "comparison: fully priced output keeps its exact known cost",
  );
}

// Empty inputs and purity.
{
  assert(buildModelCostComparison([], []).length === 0, "comparison: empty");
  const main = [modelUsage("pure", true, [2, 3, 4, 5], [10, 20, 30, 40])];
  const sub = [modelUsage("pure", false, [0, 0, 0, 0], [1, 2, 3, 4], 0.25)];
  const before = JSON.stringify({ main, sub });
  buildModelCostComparison(main, sub);
  assert(JSON.stringify({ main, sub }) === before, "comparison: inputs not mutated");
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
  // Per-call (delta) stacking: each call's own cost, not the running total.
  assert(s.deltaLayers.length === s.segments.length, "one delta layer per segment");
  assert(approx(s.deltaLayers[2][0], 5), "delta input boundary at call 1");
  assert(approx(s.deltaLayers[3][0], 7.5), "delta top at call 1");
  assert(approx(s.deltaLayers[0][1], 1), "delta cacheRead boundary at call 2");
  assert(approx(s.deltaLayers[2][1], 1), "delta input adds nothing at call 2");
  assert(approx(s.deltaLayers[3][1], 6), "delta top at call 2 is that call's cost");
  assert(approx(s.maxDelta, 7.5), "maxDelta = most expensive call");
}

// Unweighted fallback: no pricing for any model -> raw token counts.
{
  const entries = [entry({ model: "mystery", input_tokens: 100, output_tokens: 50 })];
  const s = buildTokenCostStack(entries, { mystery: null });
  assert(!s.weighted, "unweighted when nothing priced");
  assert(approx(s.maxY, 150), "maxY is raw tokens");
  assert(approx(s.maxDelta, 150), "maxDelta is raw tokens");
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

// A failed/new pricing lookup must not erase earlier confirmed unknowns or
// count unavailable pricing as a confirmed unpriced model.
{
  const costs = { known: opusCost, unknown: null };
  const entries = [
    entry({ model: "known" }),
    entry({ model: "unknown" }),
    entry({ model: "new-model" }),
  ];
  assert(
    countConfirmedUnpricedCalls(entries, costs) === 1,
    "pricing failure preserves confirmed unknown model",
  );
  assert(
    countConfirmedUnpricedCalls(entries, {}) === 0,
    "failed initial lookup is not confirmed unpriced",
  );
  assert(
    countConfirmedUnpricedCalls(
      [
        { model: "unknown", llm_calls: 4 },
        { model: "new-model", llm_calls: 3 },
      ],
      costs,
    ) === 4,
    "indirect call counts preserve confirmed unknowns",
  );
  assert(
    countConfirmedUnpricedCalls([{ model: "" }, {}], {}) === 2,
    "missing model names cannot be priced",
  );
  assert(
    countConfirmedUnpricedCalls([{ model: "unknown", llm_calls: 0 }], costs) === 0,
    "zero calls stay zero",
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
  assert(s.n === 0 && s.maxY === 0 && s.maxDelta === 0, "empty stack");
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

// Per-call bars are the cells of a fixed grid, about a pixel or more wide:
// every call sits inside its own bar, bars never overlap, and calls sharing
// a cell share a bar that draws (and hovers as) the most expensive of them.
{
  const check = (xs: number[], hs: number[]) => {
    const bars = deltaBars(xs, hs);
    const width = bars.length ? bars[0].right - bars[0].left : 0;
    return (
      bars.every((b, k) => k === 0 || bars[k - 1].right <= b.left + 1e-12) &&
      bars.every((b) => approx(b.right - b.left, width) && b.left >= 0 && b.right <= 1 + 1e-12) &&
      bars.every(
        (b, k) => b.count > 0 && b.first === (k ? bars[k - 1].first + bars[k - 1].count : 0),
      ) &&
      bars.reduce((sum, b) => sum + b.count, 0) === xs.length &&
      bars.every((b) =>
        xs
          .slice(b.first, b.first + b.count)
          .every((x) => b.left - 1e-12 <= x && x <= b.right + 1e-12),
      ) &&
      bars.every((b) => hs[b.call] === Math.max(...hs.slice(b.first, b.first + b.count)))
    );
  };

  // Sparse: a separate capped-width bar per call, containing it.
  const sparse = deltaBars(callXLayout(5).xs, [1, 2, 3, 4, 5]);
  assert(sparse.length === 5 && check(callXLayout(5).xs, [1, 2, 3, 4, 5]), "sparse: bar per call");
  assert(sparse[0].right - sparse[0].left < 0.04, "sparse bars are capped");
  assert(sparse[1].left - sparse[0].right > 0.1, "sparse bars stay separate");
  assert(approx(sparse[0].left, 0) && approx(sparse[4].right, 1), "edge bars stay in the plot");

  // Bar count tracks what fits, with no sudden halving.
  for (const count of [2, 33, 34, 200, 333, 334, 400, 667, 1000, 2000]) {
    const xs = callXLayout(count).xs;
    const got = deltaBars(xs, new Array(count).fill(1)).length;
    assert(got === Math.min(count, 333), `${count} calls draw ${Math.min(count, 333)} bars`);
    assert(check(xs, new Array(count).fill(1)), `${count} calls keep bar invariants`);
  }
  const mid = deltaBars(callXLayout(200).xs, new Array(200).fill(1));
  assert(approx(mid[1].left, mid[0].right), "moderate bars are contiguous");

  // Dense: the most expensive call wins its bar.
  const heights = Array.from({ length: 2000 }, (_, i) => (i % 7) + (i === 1001 ? 100 : 0));
  assert(
    deltaBars(callXLayout(2000).xs, heights).some((b) => b.call === 1001),
    "the most expensive call wins its bar",
  );

  // Time layouts: clustered and tied calls never push a bar off its call.
  const clustered = [0.029, 0.031, 0.061, 1];
  assert(check(clustered, [1, 1, 1, 1]), "clustered calls sit inside their bars");
  const tied = deltaBars([0, 0, 1], [1, 3, 2]);
  assert(tied.length === 2 && tied[0].call === 1 && tied[0].count === 2, "tied calls share a bar");
  // Generation boundaries fall strictly between the old generation's last
  // call and the new one's first: on a bar edge when they're in separate
  // bars, else midway (inside the bar they share).
  for (const [xs, gens] of [
    [callXLayout(5).xs, [1, 1, 2, 2, 2]],
    [callXLayout(200).xs, Array.from({ length: 200 }, (_, i) => (i < 77 ? 1 : 2))],
    [
      [0, 0.99, 1],
      [1, 2, 3],
    ],
    [
      [0, 0.98, 1],
      [1, 1, 2],
    ],
  ] as [number[], number[]][]) {
    const bars = deltaBars(
      xs,
      xs.map(() => 1),
    );
    const lines = gens.flatMap((g, i) => (i > 0 && g !== gens[i - 1] ? [i] : []));
    const xsAt = lines.map((i) => generationBoundary(bars, xs, i));
    assert(
      lines.every((i, k) => xs[i - 1] < xsAt[k] && xsAt[k] <= xs[i]),
      `generation lines between their calls (${xs.length} calls)`,
    );
    assert(new Set(xsAt).size === xsAt.length, "distinct generation lines stay distinct");
    assert(
      lines.every(
        (i, k) =>
          bars.some((b) => b.first <= i - 1 && i < b.first + b.count) ||
          bars.some((b) => approx(b.left, xsAt[k])),
      ),
      "lines between separate bars sit on a bar edge",
    );
  }
  assert(deltaBars([], []).length === 0, "no bars for no calls");
  const one = deltaBars([0.5], [1]);
  assert(one.length === 1 && one[0].left <= 0.5 && one[0].right >= 0.5, "single call bar");

  // Seeded property check over random time-like layouts.
  let seed = 7;
  const rand = () => ((seed = (seed * 16807) % 2147483647) - 1) / 2147483646;
  for (let trial = 0; trial < 500; trial++) {
    const count = 1 + Math.floor(rand() * (trial % 2 ? 40 : 1500));
    const xs = Array.from({ length: count }, () => (rand() < 0.2 ? 1 : rand() < 0.1 ? 0 : rand()));
    xs.sort((a, b) => a - b);
    if (
      !check(
        xs,
        xs.map(() => Math.floor(rand() * 5)),
      )
    ) {
      assert(false, `random layout ${trial} keeps bar invariants`);
      break;
    }
  }
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

// stackedAreaPaths: one path per layer, a subpath per turn, slabs for
// zero-width turns, all within the plot's pixel bounds.
{
  const geom = { left: 10, right: 110, yAt: (v: number) => 100 - v };
  const layers = [
    [0, 10, 20],
    [5, 30, 60],
  ];
  const paths = stackedAreaPaths(layers, callXLayout(3), geom);
  assert(paths.length === 2, "stackedAreaPaths: a path per layer");
  assert(
    paths[0] === "M10.0,100.0L60.0,90.0L110.0,80.0L110.0,100.0L60.0,100.0L10.0,100.0Z",
    `stackedAreaPaths: bottom layer rests on zero (${paths[0]})`,
  );
  assert(
    paths[1] === "M10.0,95.0L60.0,70.0L110.0,40.0L110.0,80.0L60.0,90.0L10.0,100.0Z",
    `stackedAreaPaths: upper layer rests on the one below (${paths[1]})`,
  );

  const turns = {
    xs: [0, 0.4, 0.6, 1],
    turns: [
      [0, 1],
      [2, 3],
    ] as [number, number][],
    activeMs: 0,
  };
  const gapped = stackedAreaPaths([[1, 2, 3, 4]], turns, geom)[0];
  assert((gapped.match(/M/g) || []).length === 2, "stackedAreaPaths: a subpath per turn");

  const single = stackedAreaPaths([[7]], callXLayout(1), geom)[0];
  assert(
    single === "M59.0,93.0L61.0,93.0L61.0,100.0L59.0,100.0Z",
    `stackedAreaPaths: a lone call is a narrow slab (${single})`,
  );
  const edge = stackedAreaPaths([[7, 7]], { xs: [0, 0], turns: [[0, 1]], activeMs: 0 }, geom)[0];
  assert(
    edge.startsWith("M10.0,") && !edge.includes("M9"),
    "stackedAreaPaths: slabs stay in the plot",
  );
}

// Context-size pricing tiers: a call whose prompt exceeds the threshold bills
// the whole call at the tier rate; a call below it stays at the base rate.
{
  const haikuTiered = {
    input: 0.1,
    output: 0.5,
    cache_read: 0.01,
    cache_write: 0.125,
    tiers: [{ context: 100_000, input: 0.5, output: 2.5, cache_read: 0.05, cache_write: 0.625 }],
  };
  // effectiveCost picks base below/at the threshold and the tier above it.
  assert(effectiveCost(haikuTiered, 50_000).input === 0.1, "effectiveCost: base below threshold");
  assert(effectiveCost(haikuTiered, 100_000).input === 0.1, "effectiveCost: base at threshold");
  assert(effectiveCost(haikuTiered, 100_001).input === 0.5, "effectiveCost: tier above threshold");
  assert(
    effectiveCost({ input: 1, output: 2, cache_read: 0, cache_write: 0 }, 9e9).input === 1,
    "effectiveCost: flat pricing ignores prompt size",
  );

  const entries = [
    // Prompt 60k (< 100k): base rate. input 60k @ $0.1/M = $0.006, output $0.025.
    entry({ model: "claude-haiku-5-5", input_tokens: 60_000, output_tokens: 50_000 }),
    // Prompt 150k (> 100k): tier rate. input 150k @ $0.5/M = $0.075, output $0.125.
    entry({ model: "claude-haiku-5-5", input_tokens: 150_000, output_tokens: 50_000 }),
  ];
  const s = buildTokenCostStack(entries, { "claude-haiku-5-5": haikuTiered });
  const inputRow = s.perModel[0].rows.find((t) => t.band.key === "input_tokens")!;
  assert(approx(inputRow.cost, 0.006 + 0.075), "tier: input cost sums base + tier calls");
  // The two input calls priced at different rates -> "mixed rates".
  assert(inputRow.unitUsdPerMtok === null, "tier: straddling calls report mixed input rate");
  const outRow = s.perModel[0].rows.find((t) => t.band.key === "output_tokens")!;
  assert(approx(outRow.cost, 0.025 + 0.125), "tier: output cost sums base + tier calls");
  assert(outRow.unitUsdPerMtok === null, "tier: mixed output rate");
  // Per-call delta: call 2 (tiered) costs more than call 1 (base) despite fewer
  // extra output tokens, because the whole prompt reprices.
  assert(approx(s.maxDelta, 0.075 + 0.125), "tier: priciest call uses tier rate");
}

// A tiered model whose every call stays below the threshold keeps a single
// (base) rate rather than reporting "mixed rates".
{
  const haikuTiered = {
    input: 0.1,
    output: 0.5,
    cache_read: 0.01,
    cache_write: 0.125,
    tiers: [{ context: 100_000, input: 0.5, output: 2.5, cache_read: 0.05, cache_write: 0.625 }],
  };
  const entries = [
    entry({ model: "claude-haiku-5-5", input_tokens: 30_000, output_tokens: 10_000 }),
  ];
  const s = buildTokenCostStack(entries, { "claude-haiku-5-5": haikuTiered });
  const inputRow = s.perModel[0].rows.find((t) => t.band.key === "input_tokens")!;
  assert(inputRow.unitUsdPerMtok === 0.1, "tier: all-below-threshold keeps base rate");
  assert(approx(inputRow.cost, 0.003), "tier: below-threshold cost uses base rate");
}

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
