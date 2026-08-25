// How loud the status bar's context token count looks, and where its steps are.
//
// The number itself is the whole warning: there used to be a warning triangle
// beside it, which said "something is wrong" about a number that is only large.

/** How loud the token count should look. "" is the ordinary status-bar color. */
export type ContextUsageLevel = "" | "warn" | "high" | "critical";

/** Human-readable name of a level, for the parts that have to say it in words
 *  rather than in color (tooltip, accessible name) — color alone is not a
 *  signal for everyone. "" has nothing to say. */
export function contextUsageLevelLabel(level: ContextUsageLevel): string {
  switch (level) {
    case "warn":
      return "getting long";
    case "high":
      return "very long";
    case "critical":
      return "extremely long";
    default:
      return "";
  }
}

// Absolute token thresholds, because that is what the number on screen says:
// "200k" going orange is legible in a way that "62% of the window" is not.
const WARN_TOKENS = 100_000;
const HIGH_TOKENS = 200_000;
const CRITICAL_TOKENS = 300_000;

// ...with a fraction-of-the-window floor, so a model with a small context
// window still colors as it fills up rather than staying plain until it
// overflows.
const WARN_FRACTION = 0.7;
const HIGH_FRACTION = 0.8;
const CRITICAL_FRACTION = 0.9;

/**
 * Level for `tokens` used out of a `maxContextTokens` window. A model that
 * declares no window (0) is judged on the absolute thresholds alone.
 */
export function contextUsageLevel(tokens: number, maxContextTokens: number): ContextUsageLevel {
  const fraction = maxContextTokens > 0 ? tokens / maxContextTokens : 0;
  if (tokens >= CRITICAL_TOKENS || fraction >= CRITICAL_FRACTION) return "critical";
  if (tokens >= HIGH_TOKENS || fraction >= HIGH_FRACTION) return "high";
  if (tokens >= WARN_TOKENS || fraction >= WARN_FRACTION) return "warn";
  return "";
}
