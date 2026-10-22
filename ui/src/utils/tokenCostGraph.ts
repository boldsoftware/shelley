// Pure math for the token cost graph: turns a conversation's per-LLM-call
// usage records into a stacked cumulative series, weighting each token type by
// its models.dev price for the model that served the call.
//
// When no model in the conversation has known pricing, the series falls back
// to raw token counts (weighted=false) so the graph still shows something.

export interface UsageEntry {
  input_tokens: number;
  cache_creation_input_tokens: number;
  cache_read_input_tokens: number;
  output_tokens: number;
  cost_usd: number;
  model?: string;
  url?: string;
  /** Short excerpt of the message this call produced (for hover context). */
  snippet?: string;
  /** Conversation generation this call belongs to (increments on compaction). */
  generation?: number;
  /** An in-place compaction rewrote the context since the previous call. */
  afterCompaction?: boolean;
  /** ms since epoch of the message, for the time x-axis. */
  timestamp?: number;
  /** When the call was sent and when it finished (RFC 3339), as recorded in
   *  its usage; they measure idle time between calls. */
  start_time?: string | null;
  end_time?: string | null;
  /** True when this call begins a new turn (first call, or first call after a
   *  user message / end-of-turn). Idle time before it is collapsed into a
   *  fixed-width gap on the time x-axis. */
  startsTurn?: boolean;
  /** ms since epoch of the human message that triggered this turn (only set
   *  when startsTurn). Anchors the turn's first call: created_at marks call
   *  completion, so without an anchor the first call of each turn would have
   *  zero duration on the time x-axis. */
  turnStartTimestamp?: number;
}

/** models.dev pricing, USD per million tokens. */
export interface ModelCost {
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
  /** Context-size pricing cliffs (ascending by context); absent for flat
   *  pricing. Once a call's prompt exceeds a tier's threshold, the whole call
   *  bills at that tier's rates. See effectiveCost. */
  tiers?: CostTier[];
}

/** The rates that replace the base rates once a call's prompt exceeds
 *  `context` tokens. Same USD-per-Mtok shape as ModelCost. */
export interface CostTier {
  context: number;
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
}

/** The prompt (context) size a pricing tier is measured against: every token
 *  sent up, i.e. fresh input plus cache writes and reads. Output isn't part of
 *  the context window. */
export function promptSize(e: UsageEntry): number {
  return e.input_tokens + e.cache_creation_input_tokens + e.cache_read_input_tokens;
}

/** The per-token rates that apply to a call whose prompt is promptTokens long,
 *  picking the highest tier the prompt exceeds. Flat-priced models (no tiers)
 *  return their base rates unchanged. Anthropic-style: crossing a threshold
 *  reprices the entire call, not just the tokens past it. */
export function effectiveCost(cost: ModelCost, promptTokens: number): ModelCost {
  let rates = cost;
  for (const t of cost.tiers ?? []) {
    // tiers are sorted ascending, so the last one we clear is the dearest.
    if (promptTokens > t.context) rates = t;
  }
  return rates;
}

/** The USD-per-Mtok price fields shared by ModelCost and CostTier; excludes
 *  the tier threshold so band lookups like cost[band.costKey] stay numeric. */
export type PriceKey = "input" | "output" | "cache_read" | "cache_write";

export interface TokenBand {
  key: keyof Pick<
    UsageEntry,
    "input_tokens" | "cache_creation_input_tokens" | "cache_read_input_tokens" | "output_tokens"
  >;
  costKey: PriceKey;
  label: string;
  /** Base hue/saturation/lightness; per-model shades derive from this. */
  hsl: [number, number, number];
}

// Bottom-to-top stacking order within a model. Cache reads dominate token
// volume in agent conversations, so they anchor the bottom of the stack.
export const TOKEN_BANDS: TokenBand[] = [
  {
    key: "cache_read_input_tokens",
    costKey: "cache_read",
    label: "Cache read",
    hsl: [199, 92, 60],
  },
  {
    key: "cache_creation_input_tokens",
    costKey: "cache_write",
    label: "Cache write",
    hsl: [234, 89, 74],
  },
  { key: "input_tokens", costKey: "input", label: "Input", hsl: [160, 64, 52] },
  { key: "output_tokens", costKey: "output", label: "Output", hsl: [27, 96, 61] },
];

// Lightness shifts per model index so every (model, band) segment gets its
// own color: model 0 keeps the base palette, later models get darker/lighter
// shades of the same band hues. Values stay within [-30, +12] so no shift
// clamps for the base lightnesses (52..74) — clamping would collide shades.
// Conversations with more than 6 models reuse colors cyclically.
const MODEL_LIGHTNESS_SHIFT = [0, -22, 12, -30, -12, 6];

/** Distinct color for a (model, band) segment. */
export function segmentColor(bandIndex: number, modelIndex: number): string {
  const [h, s, l] = TOKEN_BANDS[bandIndex].hsl;
  const shift = MODEL_LIGHTNESS_SHIFT[modelIndex % MODEL_LIGHTNESS_SHIFT.length];
  return `hsl(${h} ${s}% ${l + shift}%)`;
}

export interface ModelUsage {
  model: string;
  priced: boolean;
  /** Per-band tokens, unit price (USD/Mtok), cost, and segment color.
   *  unitUsdPerMtok is null when a tiered model served calls at more than one
   *  rate for this band ("mixed rates"); the cost still totals them exactly. */
  rows: {
    band: TokenBand;
    tokens: number;
    unitUsdPerMtok: number | null;
    cost: number;
    color: string;
  }[];
  totalCost: number;
  /** Provider-reported cost sum (gateway header); fallback when unpriced. */
  reportedUsd: number;
}

export interface ModelCostColumn {
  knownUsd: number;
  /** Provider-reported fallback from unpriced contributors only. */
  reportedUsd: number;
  priced: boolean;
  rows: {
    band: TokenBand;
    tokens: number;
    cost: number;
    priced: boolean;
    unitUsdPerMtok: number | null;
    color: string;
  }[];
}

export interface ModelCostComparison {
  model: string;
  main?: ModelCostColumn;
  subagents?: ModelCostColumn;
}

function buildModelCostColumn(usages: ModelUsage[]): ModelCostColumn {
  const priced = usages.every((usage) => usage.priced);
  return {
    knownUsd: usages.reduce(
      (sum, usage) => sum + (usage.priced ? usage.totalCost : usage.reportedUsd),
      0,
    ),
    reportedUsd: usages.reduce((sum, usage) => sum + (usage.priced ? 0 : usage.reportedUsd), 0),
    priced,
    rows: usages[0].rows.map((firstRow) => {
      const rows = usages.map(
        (usage) => usage.rows.find((row) => row.band.key === firstRow.band.key)!,
      );
      const contributing = rows.flatMap((row, index) => (row.tokens > 0 ? [index] : []));
      // Empty bands retain their catalog rates; used bands only compare
      // endpoints which contributed tokens of that type.
      const indexes = contributing.length ? contributing : rows.map((_, index) => index);
      const bandPriced = indexes.every((index) => usages[index].priced);
      const rate = rows[indexes[0]].unitUsdPerMtok;
      return {
        band: firstRow.band,
        tokens: rows.reduce((sum, row) => sum + row.tokens, 0),
        cost: rows.reduce((sum, row, i) => sum + (usages[i].priced ? row.cost : 0), 0),
        priced: bandPriced,
        unitUsdPerMtok:
          bandPriced && indexes.every((index) => rows[index].unitUsdPerMtok === rate) ? rate : null,
        color: firstRow.color,
      };
    }),
  };
}

/** Align main-conversation and sub-agent costs into one row per model. */
export function buildModelCostComparison(
  main: ModelUsage[],
  subagents: ModelUsage[],
): ModelCostComparison[] {
  const byModel = new Map<string, { main: ModelUsage[]; subagents: ModelUsage[] }>();
  for (const usage of main) {
    const grouped = byModel.get(usage.model) || { main: [], subagents: [] };
    grouped.main.push(usage);
    byModel.set(usage.model, grouped);
  }
  for (const usage of subagents) {
    const grouped = byModel.get(usage.model) || { main: [], subagents: [] };
    grouped.subagents.push(usage);
    byModel.set(usage.model, grouped);
  }
  return [...byModel].map(([model, grouped]) => ({
    model,
    ...(grouped.main.length && { main: buildModelCostColumn(grouped.main) }),
    ...(grouped.subagents.length && { subagents: buildModelCostColumn(grouped.subagents) }),
  }));
}

/** One stacked layer: a (model, band) pair with its own color. */
export interface StackSegment {
  model: string;
  band: TokenBand;
  color: string;
}

export interface TokenCostStack {
  /** Number of x points (LLM calls). */
  n: number;
  /** Segments bottom-to-top: models in first-seen order, bands within. */
  segments: StackSegment[];
  /** Per-segment cumulative upper boundary, stacked bottom-to-top:
   *  layers[s][i] = sum over segments 0..s of cumulative value at call i. */
  layers: number[][];
  /** Top of the stack at the last point (total weighted value). */
  maxY: number;
  /** Per-segment per-call upper boundary, stacked bottom-to-top:
   *  deltaLayers[s][i] = sum over segments 0..s of call i's own value. */
  deltaLayers: number[][];
  /** Tallest per-call stack (the most expensive single call). */
  maxDelta: number;
  /** True when values are dollars; false when raw token counts. */
  weighted: boolean;
  /** Per-model band breakdowns, in first-seen order. */
  perModel: ModelUsage[];
  /** Sum of provider-reported cost_usd (0 if never reported). */
  reportedCostUsd: number;
}

export function buildTokenCostStack(
  entries: UsageEntry[],
  costs: Record<string, ModelCost | null | undefined>,
): TokenCostStack {
  // Weight by dollars when at least one model has pricing; otherwise fall
  // back to raw token counts.
  const weighted = entries.some((e) => e.model && costs[e.model]);

  // First pass: models in first-seen order define segment stacking.
  const modelIndex = new Map<string, number>();
  for (const e of entries) {
    const model = e.model || "unknown model";
    if (!modelIndex.has(model)) modelIndex.set(model, modelIndex.size);
  }

  const segments: StackSegment[] = [];
  const perModel: ModelUsage[] = [];
  // Per (model, band) running record of the rate charged so far, so a band
  // served at two tiers reports "mixed rates" rather than a single price.
  // undefined = no priced tokens seen yet; null = seen more than one rate.
  const seenRate: (number | null | undefined)[][] = [];
  for (const [model, mi] of modelIndex) {
    const cost = costs[model];
    perModel.push({
      model,
      priced: !!cost,
      rows: TOKEN_BANDS.map((band, b) => ({
        band,
        tokens: 0,
        unitUsdPerMtok: cost ? cost[band.costKey] : 0,
        cost: 0,
        color: segmentColor(b, mi),
      })),
      totalCost: 0,
      reportedUsd: 0,
    });
    seenRate[mi] = TOKEN_BANDS.map(() => undefined);
    TOKEN_BANDS.forEach((band, b) => {
      segments.push({ model, band, color: segmentColor(b, mi) });
    });
  }

  const layers: number[][] = segments.map(() => new Array(entries.length).fill(0));
  const deltaLayers: number[][] = segments.map(() => new Array(entries.length).fill(0));
  const running = new Array(segments.length).fill(0);
  const call = new Array(segments.length).fill(0);
  let maxDelta = 0;
  let reportedCostUsd = 0;

  entries.forEach((e, i) => {
    const model = e.model || "unknown model";
    const mi = modelIndex.get(model)!;
    const cost = costs[model];
    // Pick the tier this call's prompt lands in; flat-priced models keep their
    // base rates. Output isn't part of the context size the tier measures.
    const rates = cost ? effectiveCost(cost, promptSize(e)) : null;
    const mu = perModel[mi];
    reportedCostUsd += e.cost_usd || 0;
    mu.reportedUsd += e.cost_usd || 0;
    call.fill(0);
    TOKEN_BANDS.forEach((band, b) => {
      const tokens = e[band.key] || 0;
      const rate = rates ? rates[band.costKey] : 0;
      const usd = rates ? tokens * (rate / 1e6) : 0;
      mu.rows[b].tokens += tokens;
      mu.rows[b].cost += usd;
      mu.totalCost += usd;
      if (rates && tokens > 0) {
        const prev = seenRate[mi][b];
        if (prev === undefined) {
          seenRate[mi][b] = rate;
          mu.rows[b].unitUsdPerMtok = rate;
        } else if (prev !== null && prev !== rate) {
          seenRate[mi][b] = null;
          mu.rows[b].unitUsdPerMtok = null;
        }
      }
      const v = weighted ? usd : tokens;
      running[mi * TOKEN_BANDS.length + b] += v;
      call[mi * TOKEN_BANDS.length + b] = v;
    });
    let acc = 0;
    let dacc = 0;
    for (let s = 0; s < segments.length; s++) {
      acc += running[s];
      layers[s][i] = acc;
      dacc += call[s];
      deltaLayers[s][i] = dacc;
    }
    maxDelta = Math.max(maxDelta, dacc);
  });

  const n = entries.length;
  const maxY = n > 0 && segments.length > 0 ? layers[segments.length - 1][n - 1] : 0;
  return {
    n,
    segments,
    layers,
    maxY,
    deltaLayers,
    maxDelta,
    weighted,
    perModel,
    reportedCostUsd,
  };
}

/** One raw "other" (indirect) LLM usage entry as stored in a message's
 *  other_usage_data JSON: a usage_data-shaped record plus the purpose that
 *  incurred it (compaction, keyword_search, slug, …). */
export interface OtherUsageEntry {
  purpose: string;
  input_tokens?: number;
  cache_creation_input_tokens?: number;
  cache_read_input_tokens?: number;
  output_tokens?: number;
  cost_usd?: number;
  model?: string;
  url?: string;
}

/** One aggregated row of "other" (indirect) LLM usage — compaction
 *  summarization, LLM-backed tools, slug generation, … — grouped by
 *  (purpose, model, url). Built client-side by aggregateOtherUsage from the
 *  other_usage_data carried on conversation messages. */
export interface OtherUsageRow {
  purpose: string;
  model: string;
  url?: string;
  llm_calls: number;
  input_tokens: number;
  cache_creation_input_tokens: number;
  cache_read_input_tokens: number;
  output_tokens: number;
  /** Provider-reported cost sum (often 0 when the gateway doesn't report). */
  cost_usd: number;
}

/** Aggregate raw other-usage entries into rows keyed by (purpose, model,
 *  url), summing token counts and reported cost and counting entries as
 *  llm_calls. Rows come out in first-seen order. */
export function aggregateOtherUsage(entries: OtherUsageEntry[]): OtherUsageRow[] {
  const byKey = new Map<string, OtherUsageRow>();
  for (const e of entries) {
    const purpose = e.purpose || "";
    const model = e.model || "";
    const url = e.url || "";
    const key = `${purpose}\x00${model}\x00${url}`;
    let row = byKey.get(key);
    if (!row) {
      row = {
        purpose,
        model,
        url,
        llm_calls: 0,
        input_tokens: 0,
        cache_creation_input_tokens: 0,
        cache_read_input_tokens: 0,
        output_tokens: 0,
        cost_usd: 0,
      };
      byKey.set(key, row);
    }
    row.llm_calls++;
    row.input_tokens += e.input_tokens || 0;
    row.cache_creation_input_tokens += e.cache_creation_input_tokens || 0;
    row.cache_read_input_tokens += e.cache_read_input_tokens || 0;
    row.output_tokens += e.output_tokens || 0;
    row.cost_usd += e.cost_usd || 0;
  }
  return [...byKey.values()];
}

export interface OtherPurposeUsage {
  purpose: string;
  llmCalls: number;
  /** Total tokens across all bands. */
  tokens: number;
  /** Cost estimated from token counts × model pricing, like the graph. */
  estimatedUsd: number;
  /** Provider-reported cost from rows whose model has no known pricing. */
  reportedUnpricedUsd: number;
  /** Provider-reported cost sum (gateway header). */
  reportedUsd: number;
  /** False when any contributing model lacks pricing (estimate incomplete). */
  priced: boolean;
}

export interface OtherUsageBreakdown {
  /** Purposes in first-seen row order. */
  perPurpose: OtherPurposeUsage[];
  totals: {
    estimatedUsd: number;
    /** Provider-reported cost from calls whose model has no known pricing. */
    reportedUnpricedUsd: number;
    /** Sum of provider-reported cost_usd. */
    reportedUsd: number;
    llmCalls: number;
    /** Calls whose model has no known pricing. */
    unpricedCalls: number;
  };
}

/** Aggregate other-usage rows into a per-purpose breakdown, estimating cost
 *  with the same tokens × price/1e6 per band formula as the graph. */
export function buildOtherUsageBreakdown(
  rows: OtherUsageRow[],
  costs: Record<string, ModelCost | null | undefined>,
): OtherUsageBreakdown {
  const byPurpose = new Map<string, OtherPurposeUsage>();
  const totals = {
    estimatedUsd: 0,
    reportedUnpricedUsd: 0,
    reportedUsd: 0,
    llmCalls: 0,
    unpricedCalls: 0,
  };
  for (const row of rows) {
    let p = byPurpose.get(row.purpose);
    if (!p) {
      p = {
        purpose: row.purpose,
        llmCalls: 0,
        tokens: 0,
        estimatedUsd: 0,
        reportedUnpricedUsd: 0,
        reportedUsd: 0,
        priced: true,
      };
      byPurpose.set(row.purpose, p);
    }
    const cost = costs[row.model];
    // These rows are already summed across calls (by purpose, model, url), so
    // we can't tell which calls crossed a context-size pricing tier; the
    // estimate uses base rates. A tiered model's indirect usage (e.g.
    // compaction of a long context) is therefore undercounted here, though
    // reported_usd still carries the gateway's exact charge. Tiering this would
    // mean pricing each raw OtherUsageEntry before aggregateOtherUsage folds it.
    for (const band of TOKEN_BANDS) {
      const tokens = row[band.key] || 0;
      p.tokens += tokens;
      if (cost) {
        const usd = tokens * (cost[band.costKey] / 1e6);
        p.estimatedUsd += usd;
        totals.estimatedUsd += usd;
      }
    }
    if (!cost) {
      p.priced = false;
      p.reportedUnpricedUsd += row.cost_usd || 0;
      totals.reportedUnpricedUsd += row.cost_usd || 0;
      totals.unpricedCalls += row.llm_calls;
    }
    p.llmCalls += row.llm_calls;
    p.reportedUsd += row.cost_usd || 0;
    totals.llmCalls += row.llm_calls;
    totals.reportedUsd += row.cost_usd || 0;
  }
  return { perPurpose: [...byPurpose.values()], totals };
}

/** Count only confirmed missing model prices, not pending/failed lookups.
 * Aggregated indirect rows carry their call count; direct rows are one call. */
export function countConfirmedUnpricedCalls(
  rows: { model?: string; llm_calls?: number }[],
  costs: Record<string, ModelCost | null | undefined>,
): number {
  return rows.reduce(
    (sum, row) => sum + (!row.model || costs[row.model] === null ? (row.llm_calls ?? 1) : 0),
    0,
  );
}

export interface CostSummaryUsage {
  estimatedUsd: number;
  reportedUnpricedUsd: number;
  unpricedCalls: number;
}

export interface CostSummary {
  conversationUsd: number;
  conversationUnpricedCalls: number;
  otherUsd: number;
  otherUnpricedCalls: number;
  subagentUsd: number;
  subagentUnpricedCalls: number;
  totalUsd: number;
  unpricedCalls: number;
}

/** Build the accounting-style total shown below the graph. Estimated model
 *  costs and provider-reported costs for otherwise-unpriced calls are both
 *  known spend, so the total includes both without double-counting. */
export function buildCostSummary(
  conversationUsd: number,
  usage: {
    other?: CostSummaryUsage;
    subagents?: CostSummaryUsage;
    conversationUnpricedCalls?: number;
  } = {},
): CostSummary {
  const usageUsd = (part?: CostSummaryUsage) =>
    part ? part.estimatedUsd + part.reportedUnpricedUsd : 0;
  const otherUsd = usageUsd(usage.other);
  const subagentUsd = usageUsd(usage.subagents);
  const conversationUnpricedCalls = usage.conversationUnpricedCalls || 0;
  const otherUnpricedCalls = usage.other?.unpricedCalls || 0;
  const subagentUnpricedCalls = usage.subagents?.unpricedCalls || 0;
  return {
    conversationUsd,
    conversationUnpricedCalls,
    otherUsd,
    otherUnpricedCalls,
    subagentUsd,
    subagentUnpricedCalls,
    totalUsd: conversationUsd + otherUsd + subagentUsd,
    unpricedCalls: conversationUnpricedCalls + otherUnpricedCalls + subagentUnpricedCalls,
  };
}

/** "$0.0042", "$0.500", "$12.35" — enough precision for small costs. */
export function formatUsd(v: number): string {
  if (v === 0) return "$0";
  if (v < 0.01) return `$${v.toFixed(4)}`;
  if (v < 1) return `$${v.toFixed(3)}`;
  if (v < 100) return `$${v.toFixed(2)}`;
  return `$${Math.round(v).toLocaleString()}`;
}

export function formatTokenCount(tokens: number): string {
  if (tokens >= 999_500_000) return `${(tokens / 1e9).toFixed(1)}B`;
  if (tokens >= 999_500) return `${(tokens / 1e6).toFixed(1)}M`;
  if (tokens >= 1e3) return `${(tokens / 1e3).toFixed(0)}k`;
  return String(tokens);
}

/** Indices where a new generation starts (excluding index 0), e.g. after a
 *  compaction. Used to draw delineator lines on the graph. */
export function generationStarts(entries: UsageEntry[]): number[] {
  const starts: number[] = [];
  for (let i = 1; i < entries.length; i++) {
    if (entries[i].generation !== entries[i - 1].generation) starts.push(i);
  }
  return starts;
}

// X-axis layouts. xs[i] is the fractional position (0..1) of call i; turns
// are inclusive [start, end] index ranges drawn as separate area polygons so
// idle time between turns shows as a gap rather than a plateau.
export interface XLayout {
  xs: number[];
  turns: [number, number][];
  /** Total active (within-turn) milliseconds; 0 for the call-number layout. */
  activeMs: number;
}

/** Call-number x axis: evenly spaced, one continuous area. */
export function callXLayout(n: number): XLayout {
  if (n === 0) return { xs: [], turns: [], activeMs: 0 };
  const xs = n === 1 ? [0.5] : Array.from({ length: n }, (_, i) => i / (n - 1));
  return { xs, turns: [[0, n - 1]], activeMs: 0 };
}

/** Where a stacked area sits: the plot spans [left, right] pixels, and yAt
 *  maps a value to its pixel row. */
export interface AreaGeometry {
  left: number;
  right: number;
  yAt: (v: number) => number;
}

/** One path per layer of a stacked area (layers[s][i] is the upper boundary
 *  of band s at point i, bottom-to-top). Each turn is a separate subpath so
 *  idle time between turns renders as a gap in time mode. Zero-width turns
 *  (single call, or all calls sharing one second-granularity timestamp)
 *  become narrow slabs so they stay visible. */
export function stackedAreaPaths(layers: number[][], lay: XLayout, g: AreaGeometry): string[] {
  const plotW = g.right - g.left;
  const px = (i: number) => (g.left + lay.xs[i] * plotW).toFixed(1);
  const lower = (s: number, i: number) => (s === 0 ? 0 : layers[s - 1][i]);
  return layers.map((layer, s) => {
    let d = "";
    for (const [a, b] of lay.turns) {
      if (lay.xs[a] === lay.xs[b]) {
        const x = g.left + lay.xs[a] * plotW;
        const hw = Math.max(1, plotW * 0.006);
        const x0 = Math.max(g.left, x - hw).toFixed(1);
        const x1 = Math.min(g.right, x + hw).toFixed(1);
        const yT = g.yAt(layer[b]).toFixed(1);
        const yB = g.yAt(lower(s, b)).toFixed(1);
        d += `M${x0},${yT}L${x1},${yT}L${x1},${yB}L${x0},${yB}Z`;
        continue;
      }
      const top: string[] = [];
      for (let i = a; i <= b; i++) top.push(`${px(i)},${g.yAt(layer[i]).toFixed(1)}`);
      const bottom: string[] = [];
      for (let i = b; i >= a; i--) bottom.push(`${px(i)},${g.yAt(lower(s, i)).toFixed(1)}`);
      d += `M${top.join("L")}L${bottom.join("L")}Z`;
    }
    return d;
  });
}

// Per-call bar width bounds, as fractions of the plot. The cap keeps sparse
// conversations from drawing a handful of calls as wide blocks; the floor
// (about a pixel at popup size) keeps long conversations' bars from
// antialiasing into faint hairlines.
const MAX_BAR_FRAC = 0.03;
const MIN_BAR_FRAC = 0.003;

/** One bar of the per-call cost graph, spanning [left, right] plot
 *  fractions. Calls closer together than a bar's width share one bar, which
 *  shows its most expensive call so every bar is one real call's stack. */
export interface DeltaBar {
  /** The call drawn (and hovered): the group's tallest, first on ties. */
  call: number;
  /** First call in the group; the group is calls first..first+count-1. */
  first: number;
  count: number;
  left: number;
  right: number;
}

/** Lay out per-call bars over call positions xs (nondecreasing, in [0, 1]).
 *  The plot is split into equal cells one bar wide (one per call when they
 *  fit, within the width bounds); each occupied cell is a bar holding the
 *  calls that fall in it. So bars never overlap and every call lies inside
 *  its own bar, wherever calls cluster. */
export function deltaBars(xs: number[], heights: number[]): DeltaBar[] {
  const n = xs.length;
  const w = Math.max(MIN_BAR_FRAC, Math.min(1 / Math.max(n, 1), MAX_BAR_FRAC));
  // The epsilons keep exact multiples (1/(1/n), i/(n-1)·(n-1)) from
  // flooring one cell low.
  const cells = Math.floor(1 / w + 1e-9);
  const bars: DeltaBar[] = [];
  let lastCell = -1;
  for (let i = 0; i < n; i++) {
    const cell = Math.min(cells - 1, Math.max(0, Math.floor(xs[i] * cells + 1e-9)));
    const bar = bars[bars.length - 1];
    if (cell === lastCell) {
      if (heights[i] > heights[bar.call]) bar.call = i;
      bar.count++;
    } else {
      bars.push({ call: i, first: i, count: 1, left: cell / cells, right: (cell + 1) / cells });
      lastCell = cell;
    }
  }
  return bars;
}

/** Plot fraction of the boundary drawn before call i (> 0), e.g. where a
 *  new generation starts. It lies in (xs[i-1], xs[i]]: on the left edge of
 *  call i's bar when that bar starts with call i, so it doesn't cut a bar;
 *  otherwise the bar holds both sides and the line goes midway. */
export function generationBoundary(bars: DeltaBar[], xs: number[], i: number): number {
  const bar = bars.find((b) => i < b.first + b.count)!;
  return bar.first === i ? bar.left : (xs[i - 1] + xs[i]) / 2;
}

// Width of the gap drawn between turns, as a fraction of the plot. Gaps are
// fixed-width (idle wall-clock time is deliberately not represented), capped
// so heavily-turned conversations still leave most of the width for data.
const TURN_GAP_FRAC = 0.025;
const MAX_TOTAL_GAP_FRAC = 0.35;

/** Time x axis: within a turn x advances with wall-clock time; between turns
 *  a fixed-width gap is inserted regardless of idle duration. */
export function timeXLayout(entries: UsageEntry[]): XLayout {
  const n = entries.length;
  if (n === 0) return { xs: [], turns: [], activeMs: 0 };
  if (n === 1) return { xs: [0.5], turns: [[0, 0]], activeMs: 0 };

  const turns: [number, number][] = [];
  let start = 0;
  for (let i = 1; i < n; i++) {
    if (entries[i].startsTurn) {
      turns.push([start, i - 1]);
      start = i;
    }
  }
  turns.push([start, n - 1]);

  // Within-turn elapsed ms per step (0 across turn boundaries). created_at
  // marks call *completion*, so consecutive-call deltas cover every call in a
  // turn except the first; the turn's trigger (human message timestamp)
  // anchors that first call. Clamp the anchor to [previous call, first call]
  // so queued messages (sent while the agent was still working) can't
  // overlap the previous turn or go negative.
  const deltas = new Array(n).fill(0);
  let activeMs = 0;
  for (const [a, b] of turns) {
    const first = entries[a].timestamp || 0;
    const anchor = entries[a].turnStartTimestamp;
    if (first && anchor) {
      const prev = a > 0 ? entries[a - 1].timestamp || 0 : 0;
      const d = first - Math.min(first, Math.max(anchor, prev));
      deltas[a] = d;
      activeMs += d;
    }
    for (let i = a + 1; i <= b; i++) {
      const d = Math.max(0, (entries[i].timestamp || 0) - (entries[i - 1].timestamp || 0));
      deltas[i] = d;
      activeMs += d;
    }
  }
  // Degenerate timestamps (all equal/missing): space calls evenly instead.
  if (activeMs === 0) {
    for (const [a, b] of turns) for (let i = a + 1; i <= b; i++) deltas[i] = 1;
  }
  const totalTime = deltas.reduce((a, d) => a + d, 0);
  // Every turn is a single call: no within-turn extent at all. Fall back to
  // even spacing so the slabs use the full width.
  if (totalTime === 0) {
    return { xs: Array.from({ length: n }, (_, i) => i / (n - 1)), turns, activeMs };
  }

  const gapCount = turns.length - 1;
  const gapFrac = gapCount > 0 ? Math.min(TURN_GAP_FRAC, MAX_TOTAL_GAP_FRAC / gapCount) : 0;
  const timeFrac = 1 - gapCount * gapFrac;

  const xs = new Array(n).fill(0);
  let cum = 0;
  let gapsBefore = 0;
  for (let i = 0; i < n; i++) {
    if (i > 0 && entries[i].startsTurn) gapsBefore++;
    cum += deltas[i];
    xs[i] = (cum / totalTime) * timeFrac + gapsBefore * gapFrac;
  }
  return { xs, turns, activeMs };
}

/** Nice round tick values in (0, maxY), for dollar/token gridlines. */
export function yTicks(maxY: number): number[] {
  if (maxY <= 0) return [];
  const raw = maxY / 4.5;
  const pow = Math.pow(10, Math.floor(Math.log10(raw)));
  let step = pow * 10;
  for (const m of [1, 2, 2.5, 5]) {
    if (pow * m >= raw) {
      step = pow * m;
      break;
    }
  }
  const ticks: number[] = [];
  for (let v = step; v <= maxY * 0.96; v += step) ticks.push(v);
  return ticks;
}

/** "42s", "12m", "3h 05m" — compact wall-clock duration. */
export function formatDuration(ms: number): string {
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  return `${h}h ${String(m % 60).padStart(2, "0")}m`;
}
