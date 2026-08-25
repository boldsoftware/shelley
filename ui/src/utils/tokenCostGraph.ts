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
  /** ms since epoch of the message, for the time x-axis. */
  timestamp?: number;
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
}

export interface TokenBand {
  key: keyof Pick<
    UsageEntry,
    "input_tokens" | "cache_creation_input_tokens" | "cache_read_input_tokens" | "output_tokens"
  >;
  costKey: keyof ModelCost;
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
  /** Per-band tokens, unit price (USD/Mtok), cost, and segment color. */
  rows: { band: TokenBand; tokens: number; unitUsdPerMtok: number; cost: number; color: string }[];
  totalCost: number;
  /** Provider-reported cost sum (gateway header); fallback when unpriced. */
  reportedUsd: number;
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
    TOKEN_BANDS.forEach((band, b) => {
      segments.push({ model, band, color: segmentColor(b, mi) });
    });
  }

  const layers: number[][] = segments.map(() => new Array(entries.length).fill(0));
  const running = new Array(segments.length).fill(0);
  let reportedCostUsd = 0;

  entries.forEach((e, i) => {
    const model = e.model || "unknown model";
    const mi = modelIndex.get(model)!;
    const cost = costs[model];
    const mu = perModel[mi];
    reportedCostUsd += e.cost_usd || 0;
    mu.reportedUsd += e.cost_usd || 0;
    TOKEN_BANDS.forEach((band, b) => {
      const tokens = e[band.key] || 0;
      const usd = cost ? tokens * (cost[band.costKey] / 1e6) : 0;
      mu.rows[b].tokens += tokens;
      mu.rows[b].cost += usd;
      mu.totalCost += usd;
      running[mi * TOKEN_BANDS.length + b] += weighted ? usd : tokens;
    });
    let acc = 0;
    for (let s = 0; s < segments.length; s++) {
      acc += running[s];
      layers[s][i] = acc;
    }
  });

  const n = entries.length;
  const maxY = n > 0 && segments.length > 0 ? layers[segments.length - 1][n - 1] : 0;
  return { n, segments, layers, maxY, weighted, perModel, reportedCostUsd };
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
