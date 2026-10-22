// Why one LLM call cost what it did, for the usage popup: its cost split by
// token type, whether its prompt missed the cache, and a short note when
// something about its prompt stands out. The usual surprise is a cache miss:
// the whole context is sent again at the cache-write price instead of the
// cache-read price, often 10x more.
import {
  effectiveCost,
  formatDuration,
  formatTokenCount,
  promptSize,
  TOKEN_BANDS,
  type DeltaBar,
  type ModelCost,
  type UsageEntry,
} from "./tokenCostGraph";

/** Share of a call's cost below which a token type is noise, not a reason. */
const NEGLIGIBLE_SHARE = 0.01;

export interface CallCostPart {
  /** Index into TOKEN_BANDS. */
  band: number;
  tokens: number;
  /** Null when the call's model has no pricing. */
  usd: number | null;
}

/** One call's token types, costliest first (most tokens first without
 *  pricing). Types with no tokens are left out, and so are, when priced,
 *  types that cost next to nothing next to the rest. */
export function callCostParts(
  entry: UsageEntry,
  cost: ModelCost | null | undefined,
): CallCostPart[] {
  const rates = cost ? effectiveCost(cost, promptSize(entry)) : null;
  const parts = TOKEN_BANDS.map((band, b) => {
    const tokens = entry[band.key] || 0;
    return { band: b, tokens, usd: rates ? (tokens * rates[band.costKey]) / 1e6 : null };
  }).filter((part) => part.tokens > 0);
  const total = parts.reduce((sum, part) => sum + (part.usd ?? 0), 0);
  return parts
    .filter((part) => part.usd === null || part.usd >= total * NEGLIGIBLE_SHARE)
    .sort((a, b) => (b.usd ?? 0) - (a.usd ?? 0) || b.tokens - a.tokens);
}

/** Re-sending this much of a prompt the previous call already sent (a share
 *  of it, and at least MISS_FLOOR tokens) is a cache miss, even when the
 *  prompt also grew more than that. */
const MISS_SHARE = 0.1;
const MISS_FLOOR = 2_000;

/** What a call's prompt was, next to what the cache could have held. A
 *  switched call is on another model than the previous call, and is read
 *  against its own model's last call. */
export type PromptReading =
  /** The model never reports caching. */
  | { kind: "uncached" }
  | { kind: "first" }
  | { kind: "compaction" }
  /** The first call on this model. */
  | { kind: "switch" }
  /** added: tokens new since the previous call; the rest were sent before. */
  | { kind: "miss"; added: number; gapMs: number | null; switched: boolean }
  | { kind: "new"; added: number; switched: boolean };

/** Bars whose call, the one drawn and hovered, missed the cache. A miss
 *  sharing a bar with a costlier call isn't marked: hovering couldn't show
 *  it. */
export function missedBars(bars: DeltaBar[], readings: PromptReading[]): DeltaBar[] {
  return bars.filter((b) => readings[b.call]?.kind === "miss");
}

/** Reads every call's prompt, in one pass. */
export function readPrompts(entries: UsageEntry[]): PromptReading[] {
  // Reads or writes anywhere show a model caches; OpenAI reports only reads.
  const caching = new Set(
    entries
      .filter((e) => e.cache_read_input_tokens + e.cache_creation_input_tokens > 0)
      .map((e) => e.model),
  );
  // Each model's last call since the last compaction.
  const lastOn = new Map<string | undefined, UsageEntry>();
  return entries.map((e, i): PromptReading => {
    const prev = entries[i - 1];
    const compacted =
      !!prev && (e.afterCompaction || (prev.generation ?? 1) !== (e.generation ?? 1));
    if (compacted) lastOn.clear();
    // Without both names there's no telling it switched.
    const switched = !!prev?.model && !!e.model && e.model !== prev.model;
    const base = switched ? lastOn.get(e.model) : prev;
    lastOn.set(e.model, e);
    if (!caching.has(e.model)) return { kind: "uncached" };
    if (!prev) return { kind: "first" };
    if (compacted) return { kind: "compaction" };
    if (!base) return { kind: "switch" };
    const fresh = e.input_tokens + e.cache_creation_input_tokens;
    const basePrompt = promptSize(base);
    if (promptSize(e) < basePrompt) {
      // Prompts only shrink at compactions, so base's usage sums several
      // requests (a turn resumed after a pause): it's no measure of the
      // prompt base sent. Only a mostly uncached prompt is a miss.
      return fresh >= MISS_FLOOR && fresh > e.cache_read_input_tokens
        ? { kind: "miss", added: 0, gapMs: switched ? null : gapMs(prev, e), switched }
        : { kind: "new", added: fresh, switched };
    }
    // Uncached tokens are new only as far as the prompt grew; the rest were
    // sent before and should have come from the cache. A known blind spot:
    // a paused turn whose first request missed sums a rewrite with a
    // read-back, so it looks like growth, and reads as new input.
    const added = Math.min(fresh, promptSize(e) - basePrompt);
    const resent = fresh - added;
    if (resent >= MISS_FLOOR && (resent > added || resent >= basePrompt * MISS_SHARE)) {
      return { kind: "miss", added, gapMs: switched ? null : gapMs(prev, e), switched };
    }
    return { kind: "new", added, switched };
  });
}

/** A short note on call e's cost, given its prompt reading r: always for a
 *  cache miss, otherwise only when its uncached prompt cost the most (so
 *  never without pricing). "" when its cost split says it all. growth names
 *  the context category that grew the most since the previous call. */
export function callNote(
  e: UsageEntry,
  r: PromptReading,
  cost: ModelCost | null | undefined,
  growth?: { label: string; tokens: number } | null,
): string {
  const tokens = formatTokenCount;
  const fresh = e.input_tokens + e.cache_creation_input_tokens;
  const cached = e.cache_read_input_tokens;
  // growth is measured from the previous call, so it describes new input
  // only when that call was on the same model.
  const mostly = (added: number) =>
    growth && !("switched" in r && r.switched) && growth.tokens >= added / 2
      ? `, mostly ${growth.label}`
      : "";
  if (r.kind === "miss") {
    const when = r.switched
      ? ` on switching back to ${e.model}`
      : r.gapMs === null
        ? ""
        : `, ${formatDuration(r.gapMs)} after the previous call`;
    // Name the new part too when it is a real share of what was written.
    const alsoNew =
      r.added >= MISS_FLOOR && r.added >= (fresh - r.added) / 4
        ? ` (${tokens(r.added)} new${mostly(r.added)})`
        : "";
    const still = cached > 0 ? tokens(cached) : "none";
    return `Cache miss${when}: ${still} of ${tokens(fresh + cached)} prompt tokens were still cached${alsoNew}.`;
  }
  if (!cost) return "";
  const rates = effectiveCost(cost, promptSize(e));
  const freshUsd =
    (e.input_tokens * rates.input + e.cache_creation_input_tokens * rates.cache_write) / 1e6;
  const others = [cached * rates.cache_read, e.output_tokens * rates.output].map((n) => n / 1e6);
  if (freshUsd <= 0 || others.some((usd) => usd > freshUsd)) return "";
  // A shared prefix may be cached already, even for a first call.
  const notYet = `${tokens(fresh)} prompt tokens weren't cached yet.`;
  switch (r.kind) {
    case "uncached":
      return `${e.model} reports no prompt caching.`;
    case "first":
      return `First call: ${notYet}`;
    case "compaction":
      return `First call after compaction: ${notYet}`;
    case "switch":
      return `First call on ${e.model}: ${notYet}`;
    case "new":
      return r.added > 0 ? `${tokens(r.added)} new prompt tokens${mostly(r.added)}.` : "";
  }
}

/** From when prev finished (its usage end time, else its message's time,
 *  which is when it was saved) to when e was sent. Without e's start time
 *  there is no telling: its message time includes its own run. */
function gapMs(prev: UsageEntry, e: UsageEntry): number | null {
  const start = Date.parse(e.start_time ?? "");
  const end = prev.end_time ? Date.parse(prev.end_time) : (prev.timestamp ?? NaN);
  if (!Number.isFinite(start) || !Number.isFinite(end) || end === 0) return null;
  return Math.max(0, start - end);
}
