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
  profiles: Profile[];
  /** The profile the settings came from; "" if none. */
  profile: string;
  /** How the settings work differently from that profile's; none if they
   *  don't, or if there's no such profile. */
  changes: SettingsDifference[];
  /** The tools turned on or off against their defaults. */
  toolChanges: SettingsDifference[];
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
  const { thinking_level, tool_overrides, compact_nudge_tokens, system_prompt } = s;
  let model = s.model;
  if (base && !base.model && model === serverDefaultModel()) model = "";
  return { model, thinking_level, tool_overrides, compact_nudge_tokens, system_prompt };
}

export function sameOverrides(
  a: Settings["tool_overrides"],
  b: Settings["tool_overrides"],
): boolean {
  const ea = Object.entries(a);
  return ea.length === Object.keys(b).length && ea.every(([k, v]) => b[k] === v);
}

/** One way settings work differently from others. */
export type SettingsDifference =
  | { kind: "model"; from: string; to: string }
  | { kind: "reasoning"; from: string; to: string }
  | { kind: "tool"; name: string; on: boolean }
  | { kind: "nudge"; from: number; to: number }
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
  tools: { name: string; default_on: boolean }[],
): SettingsDifference[] {
  const model = (s: Settings) => s.model || serverDefaultModel();
  // null for a model without reasoning, or one not in the catalog; "" for the
  // model's own choice.
  const level = (s: Settings) => {
    const m = models.find((candidate) => candidate.id === model(s));
    return m?.supports_reasoning ? s.thinking_level || m.default_reasoning_level || "" : null;
  };
  const on = (s: Settings, tool: string) =>
    s.tool_overrides[tool]
      ? s.tool_overrides[tool] === "on"
      : !!tools.find((t) => t.name === tool)?.default_on;
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
  if (from.system_prompt !== to.system_prompt) out.push({ kind: "systemPrompt" });
  return out;
}
