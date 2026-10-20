// Display categories for the context graph: which of the reconstructed
// per-call parts (see contextComposition.ts) share a color and legend row,
// plus the stacked layers the graph draws from them.
import { formatTokenCount, type ModelCost } from "../../utils/tokenCostGraph";
import type { Composition, Point } from "./contextComposition";

export type ContextCategory = { key: string; label: string; color: string };

const TEXT_PARTS = ["user", "assistant", "reasoning"];

const BASH_CATEGORIES = [
  "bash:code search",
  "bash:file read",
  "bash:build/test",
  "bash:script/query",
  "bash:system",
  "bash:other",
] as const;

const TOOL_CATEGORIES = ["repo/read", "repo/edit", "tool:browser/web", "tool:other"] as const;

const CATEGORY_LABELS: Record<string, string> = {
  text: "text",
  "bash:code search": "bash · code search",
  "bash:file read": "bash · file read",
  "bash:build/test": "bash · build/test",
  "bash:script/query": "bash · script/query",
  "bash:system": "bash · system",
  "repo/read": "repo/read",
  "repo/edit": "repo/edit",
  "bash:other": "bash · general",
  "tool:browser/web": "browser/web",
  "tool:other": "other tools",
};

const CATEGORY_COLORS: Record<string, string> = {
  // Cost graph-adjacent blue, purple, teal, and orange hues, with spaced
  // shades for neighboring context bands.
  text: "hsl(174 58% 48%)",
  "bash:code search": "hsl(199 92% 56%)",
  "bash:file read": "hsl(199 68% 66%)",
  "bash:build/test": "hsl(234 75% 59%)",
  "bash:script/query": "hsl(350 66% 56%)",
  "bash:system": "hsl(27 96% 57%)",
  "repo/read": "hsl(190 55% 50%)",
  "repo/edit": "hsl(45 80% 54%)",
  "bash:other": "hsl(0 0% 54%)",
  "tool:browser/web": "hsl(270 58% 56%)",
  "tool:other": "hsl(213 15% 53%)",
};

function category(key: string): ContextCategory {
  return { key, label: CATEGORY_LABELS[key], color: CATEGORY_COLORS[key] };
}

/** The categories present in points, bottom-to-top in stacking order. */
export function contextCategories(points: Point[]): ContextCategory[] {
  const keys = new Set<string>();
  for (const point of points) {
    for (const key of Object.keys(point.parts)) keys.add(key);
  }
  return [
    ...(TEXT_PARTS.some((key) => keys.has(key)) ? [category("text")] : []),
    ...BASH_CATEGORIES.filter((key) => key !== "bash:other" && keys.has(key)).map(category),
    ...TOOL_CATEGORIES.slice(0, 2)
      .filter((key) => keys.has(key))
      .map(category),
    ...(keys.has("bash:other") ? [category("bash:other")] : []),
    ...TOOL_CATEGORIES.slice(2)
      .filter((key) => keys.has(key))
      .map(category),
  ];
}

/** Tokens a category holds at one call; "text" folds user, assistant and
 *  reasoning together. */
export function categoryTokens(point: Point, key: string): number {
  return compositionTokens(point.parts, key);
}

/** What re-writing a cold cache would cost at this call: the context (or one
 *  category of it) at the model's cache-write price. Null when unpriced. */
export function rewarmUsd(
  point: Point,
  costs: Record<string, ModelCost | null | undefined>,
  key?: string,
): number | null {
  const cost = point.model ? costs[point.model] : null;
  if (!cost) return null;
  const tokens = key === undefined ? point.total : categoryTokens(point, key);
  return (tokens * cost.cache_write) / 1e6;
}

function compositionTokens(parts: Composition, key: string): number {
  if (key === "text") return TEXT_PARTS.reduce((sum, part) => sum + (parts[part] || 0), 0);
  return parts[key] || 0;
}

/** The category that grew the most between the prompts of calls i-1 and i:
 *  what call i's new prompt tokens mostly were (the previous call's response
 *  and the tool results it got back). Null across a compaction, which
 *  rewrites the context rather than adding to it. */
export function promptGrowth(
  points: Point[],
  i: number,
  categories: ContextCategory[],
): { label: string; tokens: number } | null {
  const cur = points[i];
  const prev = points[i - 1];
  if (!cur || !prev || cur.segment !== prev.segment) return null;
  let best: { label: string; tokens: number } | null = null;
  for (const c of categories) {
    const tokens = compositionTokens(cur.prompt, c.key) - compositionTokens(prev.prompt, c.key);
    if (tokens > (best?.tokens ?? 0)) best = { label: c.label, tokens };
  }
  return best;
}

/** Stacked upper boundaries, bottom-to-top: layers[c][i] is the sum over
 *  categories 0..c at call i (the same shape as TokenCostStack.layers). */
export function contextLayers(points: Point[], categories: ContextCategory[]): number[][] {
  const running = new Array<number>(points.length).fill(0);
  return categories.map((c) =>
    points.map((point, i) => (running[i] += categoryTokens(point, c.key))),
  );
}

/** Indexes where a compaction (new generation or in-place record) starts a
 *  new segment of the context. */
export function contextSegmentStarts(points: Point[]): number[] {
  const starts: number[] = [];
  for (let i = 1; i < points.length; i++) {
    if (points[i].segment !== points[i - 1].segment) starts.push(i);
  }
  return starts;
}

/** What a category is made of at one call, for its legend tooltip. */
export function categoryHint(key: string, point: Point): string {
  if (key === "text") {
    return TEXT_PARTS.map((part) => `${part} ${formatTokenCount(point.parts[part] || 0)}`).join(
      " · ",
    );
  }
  const breakdown = point.toolBreakdown[key];
  if (!breakdown) return "Tool output";
  const details = Object.entries(breakdown)
    .filter(([, tokens]) => tokens > 0)
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([name, tokens]) => `${name} ${formatTokenCount(tokens)}`)
    .join(" · ");
  return details || "Tool output";
}
