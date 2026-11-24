// Messaging for the "no models configured" state.
//
// When the server serves an empty model list the composer cannot send
// anything. Shelley used to paper over this by defaulting the picker to a
// hardcoded "claude-sonnet-4.6", which made the UI look usable and produced a
// confusing server error ("Unsupported model: claude-sonnet-4.6") naming a
// model the user never chose. Instead we explain what is missing and offer
// one-click exe.dev CLI suggestions.
//
// The server classifies the cause (see server/model_setup_hint.go) and sends a
// stable token in init data as `model_setup_hint`; this module maps that token
// to i18n keys so translations live in the normal i18n bundles.
import type { TranslationKeys } from "../i18n/types";

export type TranslationKey = keyof TranslationKeys;

export interface ModelSetupAction {
  // exe.dev CLI command, rendered as a click-to-apply suggest link. The
  // command itself is the link text (see sshCommandLine) so the user can see
  // exactly what will run, and can copy it if they'd rather use a terminal.
  // Not translatable: CLI syntax is CLI syntax in every locale.
  command: string;
}

export interface ModelSetupHintCopy {
  title: TranslationKey;
  note?: TranslationKey;
  actions: ModelSetupAction[];
  footer?: TranslationKey;
}

// Both remedies are offered per integration because `integrations add` fails
// with "name already in use" when the integration exists but is merely
// detached — the state that caused the production reports.
const REFLECTION_ADD: ModelSetupAction = {
  command: "integrations add reflection --name reflection --fields all --attach auto:all",
};
const REFLECTION_ATTACH: ModelSetupAction = {
  command: "integrations attach reflection auto:all",
};
const LLM_ADD: ModelSetupAction = {
  command: "integrations add llm --name llm --attach auto:all",
};
const LLM_ATTACH: ModelSetupAction = {
  command: "integrations attach llm auto:all",
};

// Both remedies for both integrations are always offered, ordered so the
// likeliest fix comes first.
//
// llm leads by default: verified on a real VM that with llm attached and
// reflection detached, Shelley still served 126 models — discovery falls back
// to probing llm.int directly when reflection fails. So llm is the integration
// that actually gates models, and reflection is the secondary fix (it restores
// discovery of *additional* / renamed llm integrations).
const EXE_LLM_FIRST: ModelSetupHintCopy = {
  title: "noModelsTitle",
  note: "noModelsExeNote",
  actions: [LLM_ATTACH, LLM_ADD, REFLECTION_ATTACH, REFLECTION_ADD],
  footer: "noModelsExeRefresh",
};

// Reflection-first ordering, for when reflection is the one known to be down
// while llm looks present.
const EXE_REFLECTION_FIRST: ModelSetupHintCopy = {
  ...EXE_LLM_FIRST,
  actions: [REFLECTION_ATTACH, REFLECTION_ADD, LLM_ATTACH, LLM_ADD],
};

const LOCAL: ModelSetupHintCopy = {
  title: "noModelsTitle",
  actions: [],
};

// onExeDev is a fallback for when hint is absent: the server only emits the
// token if the catalog was already empty at page load, but the catalog can
// empty later (integration detached, then Refresh). Without this an exe.dev
// user would be told to add a model by hand instead of fixing the integration
// that actually broke.
export function modelSetupHintKeys(hint: string | undefined, onExeDev = false): ModelSetupHintCopy {
  switch (hint) {
    case "exe_both_missing":
      // Neither source is reachable. Attaching llm alone restores models, so
      // it leads.
      return EXE_LLM_FIRST;
    case "exe_reflection_missing":
      return EXE_REFLECTION_FIRST;
    case "exe_llm_missing":
      return EXE_LLM_FIRST;
    case "exe_unknown":
      // Both sources look healthy but there are still no models. Offer the
      // remedies anyway (cheap, and covers a partial/detached config) rather
      // than leaving the user with no next step.
      return EXE_LLM_FIRST;
    case "add_model":
      return LOCAL;
    default:
      // Unknown or absent token (older server, or the catalog emptied after
      // page load). On exe.dev the integrations are the likely cause; off it,
      // the model picker is the only lever.
      return onExeDev ? EXE_LLM_FIRST : LOCAL;
  }
}

// sshCommandLine renders the command as the user would type it, which is also
// the link text. Showing the real command (rather than a "Attach llm" button)
// means the user can see what a click will do before clicking, and can copy it
// into a terminal instead if they prefer.
export function sshCommandLine(command: string): string {
  return `ssh exe.dev ${command}`;
}

// suggestURL builds an exe.dev click-to-apply link for a CLI command. Full
// encoding matters: flag values contain characters ("&", "#", spaces) that
// would otherwise truncate the command in the query string.
export function suggestURL(command: string): string {
  return `https://exe.dev/suggest?command=${encodeURIComponent(command)}`;
}

export function savedProviderSourceID(route: string): string | undefined {
  const parts = route.split(":");
  if (parts.length !== 3 || parts[0] !== "provider" || !parts[1] || !parts[2]) return undefined;
  try {
    const base64 = parts[1].replace(/-/g, "+").replace(/_/g, "/");
    const bytes = Uint8Array.from(
      atob(base64 + "=".repeat((4 - (base64.length % 4)) % 4)),
      (character) => character.charCodeAt(0),
    );
    return new TextDecoder("utf-8", { fatal: true }).decode(bytes) || undefined;
  } catch {
    return undefined;
  }
}

// canSendWithModel gates sending on having a model the server actually
// serves. This replaces the old hardcoded-fallback behavior: a model id that
// isn't in the ready list (empty selection, or a stale id left in
// localStorage from when the integrations were healthy) must not be POSTed,
// because the server rejects it with a confusing "Unsupported model" naming
// an id the user never picked.
//
// readyModelIds omitted/undefined means "unknown" (e.g. not loaded yet); we
// then only require a non-empty selection rather than blocking every send.
export function canSendWithModel(
  model: string | undefined,
  readyModelIds?: readonly string[],
  providerIds?: readonly string[],
): boolean {
  if (!model || model.trim() === "") return false;
  if (model.startsWith("provider:")) {
    if (providerIds === undefined) return true;
    const sourceID = savedProviderSourceID(model);
    return !!sourceID && providerIds.includes(sourceID);
  }
  if (!readyModelIds) return true;
  return readyModelIds.includes(model);
}

// Slash commands handled entirely client-side, or server-side without running
// the LLM. These stay usable with no model configured, so a user can still
// inspect diffs, fork, archive and rename while fixing setup.
//
// Exact match only: sendMessage's fast paths for these also match exactly, so
// a malformed "/fork junk" falls through to a real chat send and does need a
// model. /rename and /tag also have local command paths with arguments.
const MODEL_FREE_EXACT = ["/fork", "/diff", "/archive", "/clear"];
const MODEL_FREE_PREFIX = ["/rename", "/tag"];

// needsModel reports whether sending this composer text requires a model.
// Shell escapes ("!cmd") and the model-free commands above do not; everything
// else — plain prompts, /new, /compact, /model — does.
export function needsModel(message: string): boolean {
  const trimmed = message.trim();
  if (trimmed.startsWith("!")) return false;
  if (MODEL_FREE_EXACT.includes(trimmed)) return false;
  for (const cmd of MODEL_FREE_PREFIX) {
    if (trimmed === cmd || trimmed.startsWith(`${cmd} `)) return false;
  }
  return true;
}
