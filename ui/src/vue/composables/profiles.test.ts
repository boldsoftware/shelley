// Self-executing tests for comparing settings with a profile's.

import type { Settings } from "../../types";
import { DEFAULT_COMPACT_NUDGE_TOKENS } from "../components/autoCompaction";

(globalThis as { window?: unknown }).window = { __SHELLEY_INIT__: { default_model: "big" } };
const { settingsDifferences, toolOn, withTool } = await import("./profiles");

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
  { name: "compact_in_place", default_on: true },
  { name: "message_user", default_on: false },
];
const base: Settings = {
  model: "",
  thinking_level: "",
  tool_overrides: {},
  compact_nudge_tokens: 0,
  idle_compaction_minutes: 0,
  idle_compaction_prompt: "",
  cache_keep_warm_minutes: 0,
  system_prompt: "",
  disable_notifications: false,
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
        tool_overrides: { bash: "on", compact_in_place: "on" },
        compact_nudge_tokens: DEFAULT_COMPACT_NUDGE_TOKENS,
      },
    ),
    [],
    "unset model, reasoning, tools, and nudge",
  );
});

run("each difference is named, from the effective values", () => {
  assertEqual(
    diff(
      { tool_overrides: { compact_in_place: "on" } },
      {
        model: "small",
        tool_overrides: { browser: "off", compact_in_place: "on" },
        compact_nudge_tokens: 250_000,
        system_prompt: "Hi.",
      },
    ),
    [
      { kind: "model", from: "big", to: "small" },
      { kind: "reasoning", from: "medium", to: "low" },
      { kind: "tool", name: "browser", on: false },
      { kind: "nudge", from: DEFAULT_COMPACT_NUDGE_TOKENS, to: 250_000 },
      { kind: "systemPrompt" },
    ],
    "model, the reasoning it brings, a tool, the nudge, the prompt",
  );
});

run("the nudge counts only with compaction on", () => {
  const off = { tool_overrides: { compact_in_place: "off" as const } };
  assertEqual(
    diff({}, { compact_nudge_tokens: 250_000 }),
    [{ kind: "nudge", from: DEFAULT_COMPACT_NUDGE_TOKENS, to: 250_000 }],
    "a nudge, compaction on by default",
  );
  assertEqual(
    diff(off, { compact_nudge_tokens: 250_000 }),
    [
      { kind: "tool", name: "compact_in_place", on: true },
      { kind: "nudge", from: DEFAULT_COMPACT_NUDGE_TOKENS, to: 250_000 },
    ],
    "turning compaction on, with a nudge",
  );
  assertEqual(
    diff({ compact_nudge_tokens: 250_000 }, { ...off, compact_nudge_tokens: 250_000 }),
    [{ kind: "tool", name: "compact_in_place", on: false }],
    "turning compaction off",
  );
  assertEqual(diff(off, { ...off, compact_nudge_tokens: 999 }), [], "a nudge, compaction off");
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

run("idle compaction remains visible when the context tool is off", () => {
  assertEqual(
    diff({}, { idle_compaction_minutes: 7 }),
    [{ kind: "idleCompaction", from: 0, to: 7 }],
    "enabling idle compaction",
  );
  assertEqual(
    diff({ idle_compaction_minutes: 7 }, {}),
    [{ kind: "idleCompaction", from: 7, to: 0 }],
    "disabling idle compaction",
  );
  assertEqual(
    diff({}, { cache_keep_warm_minutes: 8 }),
    [{ kind: "keepWarm", from: 0, to: 8 }],
    "enabling cache warming",
  );
  assertEqual(
    diff({}, { disable_notifications: true }),
    [{ kind: "notifications", on: false }],
    "disabling notifications",
  );
  assertEqual(
    diff({}, { idle_compaction_prompt: "Keep dates." }),
    [{ kind: "idleCompactionPrompt" }],
    "changing the idle compaction prompt",
  );
});

run("a tool is on by its override, else by default, as on the server", () => {
  assertEqual(toolOn({}, "bash", tools), true, "default on");
  assertEqual(toolOn({}, "message_user", tools), false, "default off");
  assertEqual(toolOn({ bash: "off" }, "bash", tools), false, "turned off");
  assertEqual(toolOn({ message_user: "on" }, "message_user", tools), true, "turned on");
  assertEqual(toolOn({}, "bash", []), false, "not yet listed");
  assertEqual(toolOn({}, "compact_in_place", []), true, "auto compaction, not yet listed");
  assertEqual(toolOn({}, "bash", tools, true), false, "all disabled");
  assertEqual(toolOn({ bash: "on" }, "bash", tools, true), true, "all disabled, but this");
});

run("turning a tool to its default drops the override", () => {
  assertEqual(withTool({ bash: "off" }, "bash", true, tools), {}, "back on");
  assertEqual(withTool({}, "bash", false, tools), { bash: "off" }, "off");
  assertEqual(withTool({ message_user: "on" }, "message_user", false, tools), {}, "back off");
  assertEqual(
    withTool({ bash: "off" }, "message_user", true, tools),
    { bash: "off", message_user: "on" },
    "others kept",
  );
  assertEqual(withTool({}, "bash", true, tools, true), { bash: "on" }, "all disabled");
  assertEqual(
    withTool({ bash: "on" }, "bash", true, tools),
    { bash: "on" },
    "already on, spelled out",
  );
});
