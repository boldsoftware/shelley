// Profiles: named settings (model, reasoning, tools, system prompt) a
// conversation starts from or switches to. See server/profiles.go.
import { ref } from "vue";
import { profilesApi } from "../../services/api";
import type { Model, Profile, Settings } from "../../types";
import { COMPACT_IN_PLACE_TOOL, DEFAULT_COMPACT_NUDGE_TOKENS } from "../components/autoCompaction";
import { serverDefaultModel } from "../components/selectedModel";

// Shared by every view of the profiles, so a save in one shows in all.
const profiles = ref<Profile[]>([]);

export async function reloadProfiles(): Promise<void> {
  profiles.value = await profilesApi.list();
}

export function useProfiles() {
  return { profiles, reloadProfiles };
}

/** What the model picker shows and does about the settings in effect: the
 *  composer's before the first send, the conversation's after. */
export interface SettingsKnob {
  /** The profiles the settings can switch to: none but its own for a
   *  project's conversation, and no project's otherwise. */
  profiles: Profile[];
  /** The settings are a project conversation's, which stay its project's. */
  inProject: boolean;
  /** The profile the settings came from; "" if none. */
  profile: string;
  /** How the settings work differently from that profile's; none if they
   *  don't, or if there's no such profile. */
  changes: SettingsDifference[];
  /** The tools turned on or off against their defaults, but for auto
   *  compaction. */
  toolChanges: SettingsDifference[];
  /** Whether auto compaction is on; the context size it first nudges the
   *  agent at; and whether idle compaction, which needs it, keeps it on. */
  autoCompaction: { on: boolean; nudge: number; needed: boolean };
  setAutoCompaction: (on: boolean) => void;
  setCompactNudge: (tokens: number) => void;
  /** Switches to the named profile's settings; to the profile they came
   *  from, it reverts them. */
  selectProfile: (name: string) => Promise<void>;
  /** Saves the settings into the profile they came from. */
  updateProfile: () => Promise<void>;
  /** Saves the settings as a new profile, which they then come from. */
  saveAsProfile: (name: string) => Promise<void>;
  configureTools: () => void;
  editProfiles: () => void;
}

/** Just the settings of a profile or ConversationSettings, as the API takes
 *  them. A model that is the server's default stays unset when it was unset
 *  in base, so the profile keeps following the default. */
export function settingsOf(s: Settings, base?: Settings): Settings {
  const {
    thinking_level,
    tool_overrides,
    compact_nudge_tokens,
    idle_compaction_minutes,
    idle_compaction_prompt,
    cache_keep_warm_minutes,
    system_prompt,
  } = s;
  let model = s.model;
  if (base && !base.model && model === serverDefaultModel()) model = "";
  return {
    model,
    thinking_level,
    tool_overrides,
    compact_nudge_tokens,
    idle_compaction_minutes,
    idle_compaction_prompt,
    cache_keep_warm_minutes,
    system_prompt,
  };
}

export function sameOverrides(
  a: Settings["tool_overrides"],
  b: Settings["tool_overrides"],
): boolean {
  const ea = Object.entries(a);
  return ea.length === Object.keys(b).length && ea.every(([k, v]) => b[k] === v);
}

type ToolDefaults = { name: string; default_on: boolean }[];

/** Whether a tool is on: as overridden, else as it is by default, which
 *  disable_all_tools makes off, as on the server (IsToolEnabled in
 *  claudetool/registry.go). Until the tools are listed, only auto compaction
 *  is on by default. */
export function toolOn(
  overrides: Record<string, string>,
  name: string,
  tools: ToolDefaults,
  disableAll = false,
): boolean {
  const o = overrides[name];
  if (o) return o === "on";
  const listed = tools.find((t) => t.name === name);
  return !disableAll && (listed ? listed.default_on : name === COMPACT_IN_PLACE_TOOL);
}

/** The overrides with a tool turned on or off, with no override when that's
 *  its default; as they were if it already is, however spelled. */
export function withTool(
  overrides: Settings["tool_overrides"],
  name: string,
  on: boolean,
  tools: ToolDefaults,
  disableAll = false,
): Settings["tool_overrides"] {
  if (toolOn(overrides, name, tools, disableAll) === on) return overrides;
  const next = { ...overrides };
  delete next[name];
  if (toolOn(next, name, tools, disableAll) !== on) next[name] = on ? "on" : "off";
  return next;
}

/** One way settings work differently from others. */
export type SettingsDifference =
  | { kind: "model"; from: string; to: string }
  | { kind: "reasoning"; from: string; to: string }
  | { kind: "tool"; name: string; on: boolean }
  | { kind: "nudge"; from: number; to: number }
  | { kind: "idleCompaction"; from: number; to: number }
  | { kind: "idleCompactionPrompt" }
  | { kind: "keepWarm"; from: number; to: number }
  | { kind: "systemPrompt" };

/** How settings `to` work differently from `from`, which is not how they're
 *  spelled differently: unset model and reasoning mean the server's and the
 *  model's defaults, reasoning means nothing to a model without it (nor to
 *  a change to or from one), a tool
 *  turned on is the same as on by default, and the compaction nudge matters
 *  only with auto compaction on in `to`. */
export function settingsDifferences(
  from: Settings,
  to: Settings,
  models: Pick<Model, "id" | "supports_reasoning" | "default_reasoning_level">[],
  tools: ToolDefaults,
): SettingsDifference[] {
  const model = (s: Settings) => s.model || serverDefaultModel();
  // null for a model without reasoning, or one not in the catalog; "" for the
  // model's own choice.
  const level = (s: Settings) => {
    const m = models.find((candidate) => candidate.id === model(s));
    return m?.supports_reasoning ? s.thinking_level || m.default_reasoning_level || "" : null;
  };
  const on = (s: Settings, tool: string) => toolOn(s.tool_overrides, tool, tools);
  // With auto compaction off, a stored nudge is idle: the default stands.
  const nudge = (s: Settings) =>
    (on(s, COMPACT_IN_PLACE_TOOL) && s.compact_nudge_tokens) || DEFAULT_COMPACT_NUDGE_TOKENS;
  const out: SettingsDifference[] = [];
  if (model(from) !== model(to)) out.push({ kind: "model", from: model(from), to: model(to) });
  // To or from a model without reasoning, the model's difference says it.
  const [levelFrom, levelTo] = [level(from), level(to)];
  if (levelFrom !== null && levelTo !== null && levelFrom !== levelTo) {
    out.push({ kind: "reasoning", from: levelFrom, to: levelTo });
  }
  const names = new Set([...Object.keys(from.tool_overrides), ...Object.keys(to.tool_overrides)]);
  for (const name of [...names].sort()) {
    if (on(from, name) !== on(to, name)) out.push({ kind: "tool", name, on: on(to, name) });
  }
  if (on(to, COMPACT_IN_PLACE_TOOL) && nudge(from) !== nudge(to)) {
    out.push({ kind: "nudge", from: nudge(from), to: nudge(to) });
  }
  if (from.idle_compaction_minutes !== to.idle_compaction_minutes) {
    out.push({
      kind: "idleCompaction",
      from: from.idle_compaction_minutes,
      to: to.idle_compaction_minutes,
    });
  }
  if (from.idle_compaction_prompt !== to.idle_compaction_prompt) {
    out.push({ kind: "idleCompactionPrompt" });
  }
  if (from.cache_keep_warm_minutes !== to.cache_keep_warm_minutes) {
    out.push({
      kind: "keepWarm",
      from: from.cache_keep_warm_minutes,
      to: to.cache_keep_warm_minutes,
    });
  }
  if (from.system_prompt !== to.system_prompt) out.push({ kind: "systemPrompt" });
  return out;
}
