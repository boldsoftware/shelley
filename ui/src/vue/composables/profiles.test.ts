// Self-executing tests for comparing settings with a profile's.

import type { Settings } from "../../types";
import { DEFAULT_COMPACT_NUDGE_TOKENS } from "../components/autoCompaction";

(globalThis as { window?: unknown }).window = { __SHELLEY_INIT__: { default_model: "big" } };
const { settingsDifferences } = await import("./profiles");

function assertEqual(actual: unknown, expected: unknown, message: string): void {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(
      `${message}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`,
    );
  }
}

function run(name: string, fn: () => void): void {
  fn();
  console.log(`✓ ${name}`);
}

const models = [
  { id: "big", supports_reasoning: true, default_reasoning_level: "medium" },
  { id: "small", supports_reasoning: true, default_reasoning_level: "low" },
  { id: "plain", supports_reasoning: false, default_reasoning_level: "medium" },
];
const tools = [
  { name: "bash", default_on: true },
  { name: "browser", default_on: true },
  { name: "compact_in_place", default_on: false },
];
const base: Settings = {
  model: "",
  thinking_level: "",
  tool_overrides: {},
  compact_nudge_tokens: 0,
  system_prompt: "",
};
const diff = (from: Partial<Settings>, to: Partial<Settings>) =>
  settingsDifferences({ ...base, ...from }, { ...base, ...to }, models, tools);

run("defaults spelled out are no difference", () => {
  assertEqual(
    diff(
      {},
      {
        model: "big",
        thinking_level: "medium",
        tool_overrides: { bash: "on" },
        compact_nudge_tokens: 999,
      },
    ),
    [],
    "unset model, reasoning, tool, and nudge (compaction off)",
  );
});

run("each difference is named, from the effective values", () => {
  assertEqual(
    diff(
      { tool_overrides: { compact_in_place: "on" } },
      {
        model: "small",
        tool_overrides: { browser: "off", compact_in_place: "on" },
        compact_nudge_tokens: 200_000,
        system_prompt: "Hi.",
      },
    ),
    [
      { kind: "model", from: "big", to: "small" },
      { kind: "reasoning", from: "medium", to: "low" },
      { kind: "tool", name: "browser", on: false },
      { kind: "nudge", from: DEFAULT_COMPACT_NUDGE_TOKENS, to: 200_000 },
      { kind: "systemPrompt" },
    ],
    "model, the reasoning it brings, a tool, the nudge, the prompt",
  );
});

run("the nudge counts only with compaction on", () => {
  assertEqual(
    diff({}, { tool_overrides: { compact_in_place: "on" }, compact_nudge_tokens: 200_000 }),
    [
      { kind: "tool", name: "compact_in_place", on: true },
      { kind: "nudge", from: DEFAULT_COMPACT_NUDGE_TOKENS, to: 200_000 },
    ],
    "turning compaction on, with a nudge",
  );
  assertEqual(
    diff({ tool_overrides: { compact_in_place: "on" }, compact_nudge_tokens: 200_000 }, {}),
    [{ kind: "tool", name: "compact_in_place", on: false }],
    "turning compaction off",
  );
});

run("reasoning means nothing to a model without it", () => {
  assertEqual(
    diff({ model: "plain" }, { model: "plain", thinking_level: "high" }),
    [],
    "a level left over",
  );
  assertEqual(
    diff({ model: "plain" }, { model: "big" }),
    [{ kind: "model", from: "plain", to: "big" }],
    "to a model with reasoning",
  );
  assertEqual(
    diff({ model: "big", thinking_level: "high" }, { model: "plain" }),
    [{ kind: "model", from: "big", to: "plain" }],
    "to a model without it",
  );
});
