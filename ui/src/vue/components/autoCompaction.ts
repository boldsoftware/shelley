// Auto compaction: the compact_in_place tool, plus context-size nudges from
// compact_nudge_tokens on (see server/compact_in_place.go).
export const COMPACT_IN_PLACE_TOOL = "compact_in_place";

// Mirrors defaultCompactNudgeTokens in server/compact_in_place.go.
export const DEFAULT_COMPACT_NUDGE_TOKENS = 250_000;

// What the "Compact in Place" button sends to the agent.
export const COMPACT_IN_PLACE_REQUEST = "Compact your context in place.";

export const COMPACT_NUDGE_CHOICES = [
  100_000, 130_000, 160_000, 200_000, 250_000, 300_000, 400_000,
];
