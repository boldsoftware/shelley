// Unit tests for the empty-model-list messaging helpers.
// Run with: tsx src/utils/modelSetupHint.test.ts
import {
  modelSetupHintKeys,
  sshCommandLine,
  canSendWithModel,
  needsModel,
  suggestURL,
} from "./modelSetupHint";
import { en } from "../i18n/en";

let passed = 0;
let failed = 0;
const failures: string[] = [];
function check(name: string, cond: boolean, detail?: unknown) {
  if (cond) {
    passed++;
  } else {
    failed++;
    failures.push(`✗ ${name}${detail !== undefined ? `\n   ${JSON.stringify(detail)}` : ""}`);
  }
}

const ALL_HINTS = [
  "add_model",
  "exe_both_missing",
  "exe_reflection_missing",
  "exe_llm_missing",
  "exe_unknown",
];

// Every hint the server can emit must resolve to real, non-empty copy. A
// missing key would render as blank space exactly when the user is stuck.
for (const hint of ALL_HINTS) {
  const c = modelSetupHintKeys(hint);
  check(`${hint} has a title key`, !!c.title && !!en[c.title]);
  if (c.note) check(`${hint} note resolves in en`, !!en[c.note]);
  if (c.footer) check(`${hint} footer resolves in en`, !!en[c.footer]);
  for (const a of c.actions) {
    // The command IS the link text now, so there is no label to translate;
    // it just has to be a real, runnable exe.dev command line.
    check(`${hint} action has a command`, !!a.command);
    check(
      `${hint} command renders as an ssh line: ${a.command}`,
      sshCommandLine(a.command) === `ssh exe.dev ${a.command}`,
    );
  }
}

// The copy has to stay short: this is an error state, and long prose buries
// the one thing the user needs to click. Cap the prose (links excluded).
for (const hint of ALL_HINTS) {
  const c = modelSetupHintKeys(hint);
  const prose = [c.title, c.note, c.footer]
    .filter((k): k is NonNullable<typeof k> => !!k)
    .map((k) => en[k])
    .join(" ");
  check(`${hint} prose is under 200 chars`, prose.length < 200, {
    len: prose.length,
    prose,
  });
  // Raw CLI commands belong in clickable suggest links, not in the prose.
  check(
    `${hint} prose has no inline CLI command`,
    !/integrations (add|attach|list)/.test(prose),
    prose,
  );
}

// exe.dev hints must offer BOTH integrations: reflection is how Shelley
// discovers models and llm is where they come from. Reflection masks the llm
// one, so fixing only reflection leaves the user still stuck.
for (const hint of ["exe_reflection_missing", "exe_llm_missing", "exe_unknown"]) {
  const cmds = modelSetupHintKeys(hint).actions.map((a) => a.command);
  check(
    `${hint} offers reflection`,
    cmds.some((c) => c.includes("reflection")),
    cmds,
  );
  check(
    `${hint} offers llm`,
    cmds.some((c) => /\bllm\b/.test(c)),
    cmds,
  );
  // `integrations add` fails with "name already in use" when the integration
  // exists but is merely detached — the production case — so both remedies
  // must be one click away.
  check(
    `${hint} offers add`,
    cmds.some((c) => c.startsWith("integrations add")),
    cmds,
  );
  check(
    `${hint} offers attach`,
    cmds.some((c) => c.startsWith("integrations attach")),
    cmds,
  );
}

// The detected cause should be actionable first.
{
  const first = modelSetupHintKeys("exe_llm_missing").actions[0].command;
  check("llm-missing leads with an llm action", /\bllm\b/.test(first), first);
  const firstRefl = modelSetupHintKeys("exe_reflection_missing").actions[0].command;
  check(
    "reflection-missing leads with a reflection action",
    firstRefl.includes("reflection"),
    firstRefl,
  );
}

// Off exe.dev there are no integrations to configure, so offer no CLI links.
{
  const c = modelSetupHintKeys("add_model");
  check("local hint offers no exe.dev commands", c.actions.length === 0);
}

// Unknown/absent tokens must still produce usable copy rather than blank.
check("unknown token still yields a title", !!modelSetupHintKeys("never-shipped").title);

// The token is only sent when the catalog is empty AT PAGE LOAD. If it empties
// later (integration detached, then Refresh) the UI has no token, but an
// exe.dev user still needs the integration remedies rather than "add a model
// by hand". onExeDev carries that independently of the token.
{
  const c = modelSetupHintKeys(undefined, true);
  check("absent token on exe.dev still offers integration actions", c.actions.length > 0);
  const local = modelSetupHintKeys(undefined, false);
  check("absent token off exe.dev offers no integration actions", local.actions.length === 0);
  // An explicit server token must win over the flag either way.
  check(
    "explicit local token beats onExeDev",
    modelSetupHintKeys("add_model", true).actions.length === 0,
  );
}

// The commands must stay in lockstep with execore's /suggest allowlist
// (integrationsAddSuggestionPrompt / integrationsAttachSuggestionPrompt).
// Anything outside those exact shapes yields a dead link: /suggest answers 400
// "command is not enabled for suggestions", which would make the instructions
// for fixing a broken VM themselves broken. execore/web_suggest_test.go
// (TestAPISuggestServesShelleySetupLinks) asserts the same strings server-side.
{
  const SUGGESTABLE = new Set([
    "integrations add reflection --name reflection --fields all --attach auto:all",
    "integrations add llm --name llm --attach auto:all",
    "integrations attach reflection auto:all",
    "integrations attach llm auto:all",
  ]);
  for (const hint of ALL_HINTS) {
    for (const a of modelSetupHintKeys(hint).actions) {
      check(
        `${hint} command is /suggest-allowlisted: ${a.command}`,
        SUGGESTABLE.has(a.command),
        a.command,
      );
    }
  }
}

// Attaching llm alone restores models (verified on a real VM: llm attached +
// reflection detached still served 126 models, because discovery probes
// llm.int directly when reflection fails). So the llm remedy must come first
// for the states where we don't specifically know reflection is the culprit —
// otherwise we lead the user with a command that won't fix anything.
for (const hint of ["exe_both_missing", "exe_llm_missing", "exe_unknown"]) {
  const first = modelSetupHintKeys(hint).actions[0];
  check(`${hint} leads with an llm remedy`, first.command.includes(" llm "), first.command);
  check(
    `${hint} leads with attach (works when merely detached)`,
    first.command.includes("attach"),
    first.command,
  );
}

// suggestURL builds the click-to-apply link the exe.dev CLI understands.
{
  const u = suggestURL("integrations attach llm auto:all");
  check(
    "suggest link points at exe.dev/suggest",
    u.startsWith("https://exe.dev/suggest?command="),
    u,
  );
  check(
    "suggest link percent-encodes the command",
    u.includes("integrations%20attach%20llm%20auto%3Aall"),
    u,
  );
  // Flags must survive encoding: a raw "&" or "#" would truncate the command.
  const flags = suggestURL("integrations add llm --name llm --attach auto:all");
  check("suggest link encodes flags", flags.includes("--name%20llm"), flags);
  check(
    "suggest link has a single query param",
    flags.split("?").length === 2 && !flags.includes("&"),
    flags,
  );
}

// The send-guard contract that replaces the deleted "claude-sonnet-4.6"
// fallback: never POST a model the server does not serve.
const READY = ["claude-opus-4.8", "gpt-5.6-sol"];
check("cannot send with empty model", canSendWithModel("", READY) === false);
check("cannot send with whitespace model", canSendWithModel("   ", READY) === false);
check("cannot send with undefined model", canSendWithModel(undefined, READY) === false);
check("can send with an available model", canSendWithModel("claude-opus-4.8", READY) === true);

// A model id left in localStorage from when the integrations were healthy
// must NOT be sendable once the list is empty. Without this the stale id
// reaches the server and produces the same misleading "Unsupported model"
// error the hardcoded fallback used to cause — just with a different id.
check(
  "cannot send a stale model when list is empty",
  canSendWithModel("claude-opus-4.8", []) === false,
);
check(
  "cannot send a model absent from the ready list",
  canSendWithModel("claude-sonnet-4.6", READY) === false,
);

// Unknown ready-list (older callers / not yet loaded): fall back to the
// non-empty check rather than blocking every send.
check(
  "undefined ready list falls back to non-empty",
  canSendWithModel("anything", undefined) === true,
);
check("undefined ready list still rejects empty", canSendWithModel("", undefined) === false);

// needsModel decides which sends are blocked with no model. Blocking too
// much would break local-only commands that never touch the LLM; blocking
// too little lets a doomed request reach the server and produce the
// confusing "Unsupported model" error again.
for (const local of [
  "/fork",
  "/diff",
  "/archive",
  "/rename my-slug",
  "/tag",
  "/tag +bug -wip",
  "/clear",
  "!ls -la",
  "!  echo hi",
  "!!still-a-shell-command",
]) {
  check(`needsModel(${local}) is false`, needsModel(local) === false);
}
for (const llm of [
  "hello",
  "/new",
  "/new do a thing",
  "/compact",
  "/model claude-opus-4.8",
  "/models",
  "/unknown-command",
]) {
  check(`needsModel(${llm}) is true`, needsModel(llm) === true);
}

// Malformed argument forms of no-argument commands ("/fork junk") are NOT
// handled by the local fast paths in sendMessage, so they fall through to a
// real chat send and therefore do need a model.
for (const malformed of ["/fork junk", "/diff junk", "/archive junk", "/clear junk"]) {
  check(`needsModel(${malformed}) is true`, needsModel(malformed) === true);
}

if (failed > 0) {
  console.error(failures.join("\n"));
  console.error(`\n${passed} passed, ${failed} failed`);
  process.exit(1);
}
console.log(`${passed} passed`);
