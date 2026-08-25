// Shared thinking-level constants/types. "default" leaves the request unset so
// the selected model's configured/provider default applies.
export type ThinkingLevel =
  | "default"
  | "off"
  | "minimal"
  | "low"
  | "medium"
  | "high"
  | "xhigh"
  | "max";

export const DEFAULT_THINKING_LEVEL: ThinkingLevel = "default";

export const THINKING_LEVELS: { value: ThinkingLevel; label: string }[] = [
  { value: "default", label: "default" },
  { value: "off", label: "off" },
  { value: "minimal", label: "minimal" },
  { value: "low", label: "low" },
  { value: "medium", label: "medium" },
  { value: "high", label: "high" },
  { value: "xhigh", label: "xhigh" },
  { value: "max", label: "max" },
];

export const CONCRETE_THINKING_LEVELS = THINKING_LEVELS.filter(
  (level): level is { value: Exclude<ThinkingLevel, "default">; label: string } =>
    level.value !== "default",
).map((level) => level.value);

// supportedThinkingLevels returns the levels a user may explicitly pick for a
// model: the advertised list when known, none when reasoning is unsupported,
// else the standard set through xhigh (max must be advertised).
export function supportedThinkingLevels(
  model: ReasoningModelCapabilities | undefined,
): readonly Exclude<ThinkingLevel, "default">[] {
  if (model?.supports_reasoning === false) return [];
  if (model?.reasoning_levels?.length) return model.reasoning_levels;
  return CONCRETE_THINKING_LEVELS.filter((level) => level !== "max");
}

// Round to the nearest supported level. Off is a mode rather than an effort
// tier, so non-off values never round to off. Equal distances round lower.
export function roundThinkingLevel(
  level: Exclude<ThinkingLevel, "default">,
  supported: readonly Exclude<ThinkingLevel, "default">[],
): Exclude<ThinkingLevel, "default"> {
  if (supported.length === 0 || supported.includes(level)) return level;
  const candidates = CONCRETE_THINKING_LEVELS.filter(
    (candidate) => supported.includes(candidate) && (level === "off" || candidate !== "off"),
  );
  if (candidates.length === 0) return level;
  const target = CONCRETE_THINKING_LEVELS.indexOf(level);
  return candidates.reduce((best, candidate) => {
    const candidateDistance = Math.abs(CONCRETE_THINKING_LEVELS.indexOf(candidate) - target);
    const bestDistance = Math.abs(CONCRETE_THINKING_LEVELS.indexOf(best) - target);
    return candidateDistance < bestDistance ? candidate : best;
  });
}

export interface ReasoningModelCapabilities {
  supports_reasoning?: boolean;
  reasoning_levels?: Exclude<ThinkingLevel, "default">[];
}

export function normalizeThinkingLevelForModel(
  level: ThinkingLevel,
  model: ReasoningModelCapabilities | undefined,
): ThinkingLevel {
  if (level === "default") return level;
  if (!model || model.supports_reasoning === false) return "default";
  if (model.reasoning_levels?.length) {
    const rounded = roundThinkingLevel(level, model.reasoning_levels);
    // Rounding can fail to land on a supported level (e.g. an off-only
    // model never attracts non-off levels); reset rather than send a level
    // the server would reject.
    return model.reasoning_levels.includes(rounded) ? rounded : "default";
  }
  // Unknown models keep the historical standard set through xhigh. Max must
  // be explicitly advertised.
  return level === "max" ? "default" : level;
}

export const THINKING_LEVEL_KEY = "shelley.thinkingLevel.v2";

// storedThinkingLevel is the user's last composer effort pick, or the
// "default" sentinel when nothing valid is stored.
export function storedThinkingLevel(): ThinkingLevel {
  const stored = localStorage.getItem(THINKING_LEVEL_KEY);
  return THINKING_LEVELS.some((l) => l.value === stored)
    ? (stored as ThinkingLevel)
    : DEFAULT_THINKING_LEVEL;
}
