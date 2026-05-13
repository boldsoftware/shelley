// Placeholder hints for the chat message input.
//
// Each hint may be restricted to desktop or mobile, and carries a probabilistic
// weight used by `pickPlaceholderHint` for weighted random selection.
//
// The default hint (`id: "default"`) has no literal text — callers should
// substitute the localized message placeholder via i18n. All other hints are
// English easter-egg style tips and are intentionally not i18n'd.

export type PlaceholderHintPlatform = "any" | "desktop" | "mobile";

export interface PlaceholderHint {
  id: string;
  /** Literal text. `undefined` for the default hint — caller supplies i18n text. */
  text?: string;
  platform: PlaceholderHintPlatform;
  weight: number;
}

export const PLACEHOLDER_HINTS: PlaceholderHint[] = [
  // The default has higher weight so the original placeholder dominates.
  { id: "default", platform: "any", weight: 6 },
  { id: "bash", text: "!bash works", platform: "any", weight: 1 },
  { id: "graph", text: "you can ask shelley for a graph", platform: "any", weight: 1 },
  {
    id: "history",
    text: "\u{1F4A1} ask shelley to search its history",
    platform: "any",
    weight: 1,
  },
  {
    id: "command-k",
    text: "\u2728 command-k for the command palette",
    platform: "desktop",
    weight: 1,
  },
];

export function hintsForPlatform(
  isMobile: boolean,
  hints: PlaceholderHint[] = PLACEHOLDER_HINTS,
): PlaceholderHint[] {
  const allowed: PlaceholderHintPlatform = isMobile ? "mobile" : "desktop";
  return hints.filter((h) => h.platform === "any" || h.platform === allowed);
}

/**
 * Pick a hint via weighted random selection. `rand` defaults to `Math.random`
 * and may be injected for tests.
 */
export function pickPlaceholderHint(
  isMobile: boolean,
  rand: () => number = Math.random,
  hints: PlaceholderHint[] = PLACEHOLDER_HINTS,
): PlaceholderHint {
  const eligible = hintsForPlatform(isMobile, hints);
  const total = eligible.reduce((s, h) => s + Math.max(0, h.weight), 0);
  if (total <= 0 || eligible.length === 0) {
    // Fall back: prefer an eligible hint if any, otherwise the first listed hint.
    return eligible[0] ?? hints[0];
  }
  let r = rand() * total;
  for (const h of eligible) {
    r -= Math.max(0, h.weight);
    if (r <= 0) return h;
  }
  return eligible[eligible.length - 1];
}
