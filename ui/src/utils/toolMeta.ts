// Tool metadata helpers mirroring the iOS client (see
// iOS/exe.dev/Support/ToolEmoji.swift, ToolHeadline.swift, and
// ToolPillsRow.swift). Used by the conversation UI to render
// consecutive tool calls as a wrapped row of tightly packed,
// color-coded "pills" instead of one full-width card per call.

// Extract the action string from a `browser` tool's input. We use this
// to subspecialize emoji/headline for the umbrella "browser" tool name,
// matching how BrowserTool.tsx picks a specialized child component.
function browserAction(input: unknown): string {
  if (typeof input !== "object" || input === null) return "";
  const v = (input as Record<string, unknown>).action;
  return typeof v === "string" ? v : "";
}

/** Emoji for a tool pill. Pass `input` so the umbrella "browser" tool can
 *  pick a per-action emoji matching BrowserTool's per-action component. */
export function toolEmoji(name: string | undefined | null, input?: unknown): string {
  if (!name) return "⚙️";
  if (name === "browser") {
    const action = browserAction(input);
    switch (action) {
      case "navigate":
        return "🌐";
      case "eval":
        return "⚡";
      case "resize":
        return "📐";
      case "screenshot":
        return "📷";
      case "console_logs":
      case "clear_console_logs":
        return "📋";
      case "screencast_start":
      case "screencast_stop":
      case "screencast_status":
        return "🎬";
    }
    // Folded-in families: emulate_*, network_*, accessibility_*, profile_*.
    if (action.startsWith("emulate")) return "📱";
    if (action.startsWith("network")) return "📡";
    if (action.startsWith("accessibility")) return "♿";
    if (action.startsWith("profile")) return "📊";
  }
  switch (name) {
    case "bash":
    case "shell":
      return "🛠️";
    case "patch":
      return "🖋️";
    case "screenshot":
    case "browser_take_screenshot":
      return "📷";
    case "read_image":
      return "🖼️";
    case "browser_navigate":
      return "🌐";
    case "browser_eval":
      return "⚡";
    case "subagent":
      return "⚡";
    case "keyword_search":
      return "🔍";
    case "browser_recent_console_logs":
    case "browser_clear_console_logs":
      return "📋";
    case "browser_emulate":
      return "📱";
    case "browser_resize":
      return "📐";
    case "browser_accessibility":
      return "♿";
    case "browser_network":
      return "📡";
    case "browser_profile":
      return "📊";
    case "browser_screencast":
      return "🎬";
    case "change_dir":
      return "📂";
    case "llm_one_shot":
      return "🤖";
    case "output_iframe":
      return "✨";
    case "web_search":
      return "🔎";
    default:
      return "⚙️";
  }
}

// Character budgets for the pill headline. Desktop affords more room
// than mobile, so we expose more of the command there. These are
// soft targets that drive *which* tokens we surface; CSS still adds an
// ellipsis as a final guard (see .tool-pill-text).
export const HEADLINE_BUDGET_WIDE = 48;
export const HEADLINE_BUDGET_NARROW = 24;

// Human-friendly tool names for the detail modal's title bar. The card
// itself shows the command/args, so the title just needs to say what
// kind of tool this is (no duplication of the command).
const TOOL_DISPLAY_NAMES: Record<string, string> = {
  bash: "Shell command",
  shell: "Shell command",
  patch: "File edit",
  change_dir: "Change directory",
  read_image: "Read image",
  keyword_search: "Keyword search",
  web_search: "Web search",
  subagent: "Subagent",
  llm_one_shot: "LLM request",
  output_iframe: "HTML preview",
  screenshot: "Screenshot",
  browser: "Browser",
};

/** Title for the tool detail modal: a human-friendly tool name, not the
 *  command (the card body already shows the command/args). */
export function toolDisplayName(name: string | undefined | null): string {
  if (!name) return "Tool";
  if (TOOL_DISPLAY_NAMES[name]) return TOOL_DISPLAY_NAMES[name];
  // browser_* etc: prettify the raw name.
  return name.replace(/_/g, " ").replace(/^\w/, (c) => c.toUpperCase());
}

// Subcommand-style programs: the meaning lives in the *second* token
// ("git diff", "go test", "npm run"). Derived from analysing ~183k
// real shell calls in the Shelley DB — git/go/npm/pnpm dominate and
// are useless shown as a bare program name.
const SUBCOMMAND_PROGS = new Set([
  "git",
  "go",
  "npm",
  "yarn",
  "pnpm",
  "bun",
  "deno",
  "cargo",
  "pip",
  "pip3",
  "docker",
  "kubectl",
  "brew",
  "apt",
  "apt-get",
  "systemctl",
  "tmux",
  "terraform",
  "gh",
]);

// Search tools: the *pattern* (first non-flag arg) is what matters.
const SEARCH_PROGS = new Set(["grep", "rg", "ag", "ack", "fgrep", "egrep"]);

// Leading wrappers that should be peeled off to reveal the real program
// ("sudo go test", "time make", "nohup ./srv").
const WRAPPER_PROGS = new Set([
  "sudo",
  "time",
  "exec",
  "nohup",
  "command",
  "env",
  "xargs",
  "watch",
]);

function basename(p: string): string {
  // Trim a trailing slash so "src/" -> "src".
  const trimmed = p.endsWith("/") ? p.slice(0, -1) : p;
  const i = trimmed.lastIndexOf("/");
  return i >= 0 ? trimmed.slice(i + 1) : trimmed;
}

// Split a command line into tokens, keeping quoted spans ("a b" or
// 'a b') together. Not a full shell parser — just enough to avoid
// chopping a quoted grep pattern in half.
function tokenize(s: string): string[] {
  const out: string[] = [];
  let cur = "";
  let quote = "";
  for (const ch of s) {
    if (quote) {
      cur += ch;
      if (ch === quote) quote = "";
    } else if (ch === '"' || ch === "'") {
      quote = ch;
      cur += ch;
    } else if (/\s/.test(ch)) {
      if (cur) {
        out.push(cur);
        cur = "";
      }
    } else {
      cur += ch;
    }
  }
  if (cur) out.push(cur);
  return out;
}

// Strip surrounding quotes from a single shell token.
function unquote(t: string): string {
  if (t.length >= 2) {
    const a = t[0];
    const b = t[t.length - 1];
    if ((a === '"' || a === "'") && a === b) return t.slice(1, -1);
  }
  return t;
}

// Peel leading noise off a shell command so the pill shows the part
// that actually carries intent. Removes, repeatedly:
//   * `cd <dir> &&` / `cd <dir>;` prefixes (~18% of real commands)
//   * inline env assignments (`FOO=bar cmd`, `KEY="x y" cmd`)
//   * wrapper programs (`sudo`, `time`, `nohup`, …)
function stripNoise(line: string): string {
  let s = line.trim();
  for (;;) {
    const before = s;
    // cd <dir> && rest   /   cd <dir> ; rest
    const cdm = s.match(/^cd\s+(?:"[^"]*"|'[^']*'|[^&;|]+?)\s*(?:&&|;)\s*(.*)$/s);
    if (cdm && cdm[1]) {
      s = cdm[1].trim();
      continue;
    }
    // leading env assignment: VAR=value (value may be quoted)
    const envm = s.match(/^[A-Za-z_][A-Za-z0-9_]*=(?:"[^"]*"|'[^']*'|\S*)\s+(.*)$/s);
    if (envm && envm[1]) {
      s = envm[1].trim();
      continue;
    }
    // leading wrapper program
    const wm = s.match(/^(\S+)\s+(.*)$/s);
    if (wm && WRAPPER_PROGS.has(wm[1]) && wm[2]) {
      s = wm[2].trim();
      continue;
    }
    if (s === before) break;
  }
  return s;
}

function isOperator(t: string): boolean {
  return t === "|" || t === "&&" || t === ";" || t === "||" || t === ">" || t === "<";
}

// A short, single-dash flag like "-C" or "-n" (not "--foo", not "-").
// These commonly take a value as the next token ("git -C /repo status").
// Long "--flag" forms are usually boolean or use "--flag=value", so we
// don't assume they consume the following token.
function isShortFlag(t: string): boolean {
  return /^-[A-Za-z]$/.test(t);
}

// First token that isn't a flag (doesn't start with "-") and isn't a
// shell control operator. When `skipFlagValues` is set, a *short*
// single-dash flag (`-C`, no `=`) is assumed to consume the following
// token as its value, so e.g. "git -C /repo status" resolves to
// "status" while "git --no-pager diff" still resolves to "diff".
function firstNonFlag(tokens: string[], skipFlagValues = false): string | undefined {
  for (let i = 0; i < tokens.length; i++) {
    const t = tokens[i];
    if (isOperator(t)) break;
    if (t.startsWith("-")) {
      if (skipFlagValues && isShortFlag(t) && i + 1 < tokens.length) i++;
      continue;
    }
    return t;
  }
  return undefined;
}

// A token that looks like a filesystem path (for cat/ls/sed/… we'd
// rather surface the file than an early flag value).
function looksLikePath(t: string): boolean {
  return t.includes("/") || /\.[A-Za-z0-9]+$/.test(t);
}

// Build the headline for a bash/shell command given a character budget.
//
// The budget drives *which tokens we surface*, not a hard character
// cut: we keep meaningful, whole tokens in the DOM (good for tooltips,
// accessibility, and matching) and let CSS ellipsis handle the final
// visual overflow. A wider budget (desktop) opts into showing one more
// token than a narrow budget (mobile) would.
function bashHeadline(command: string, maxLen: number): string {
  // First meaningful line (skip blanks and shell comments).
  const firstLine =
    command
      .split(/\r?\n/)
      .map((l) => l.trim())
      .find((l) => l.length > 0 && !l.startsWith("#")) || "";
  if (!firstLine) return "shell";

  const cleaned = stripNoise(firstLine);
  const tokens = tokenize(cleaned);
  if (tokens.length === 0) return firstLine;

  const prog = tokens[0].includes("/") ? basename(tokens[0]) : tokens[0];
  const rest = tokens.slice(1);

  // Subcommand programs: "git diff", "go test", "npm run dev".
  if (SUBCOMMAND_PROGS.has(prog)) {
    const sub = firstNonFlag(rest, true);
    if (sub) {
      let head = `${prog} ${unquote(sub)}`;
      // Try to append one more meaningful token (e.g. the script name
      // after "npm run", or a ref after "git show") if it fits the budget.
      const after = rest.slice(rest.indexOf(sub) + 1);
      const extra = firstNonFlag(after);
      if (extra) {
        const candidate = `${head} ${unquote(extra)}`;
        if (candidate.length <= maxLen) head = candidate;
      }
      return head;
    }
    return prog;
  }

  // Search tools: surface the pattern.
  if (SEARCH_PROGS.has(prog)) {
    const pat = firstNonFlag(rest);
    if (pat) return `${prog} ${unquote(pat)}`;
    return prog;
  }

  // Everything else: prefer a path-like argument, else first non-flag.
  const nonFlags = rest.filter(
    (t) => !t.startsWith("-") && t !== "|" && t !== "&&" && t !== ";" && t !== "||",
  );
  const pathArg = nonFlags.find((t) => looksLikePath(t));
  const arg = pathArg || firstNonFlag(rest);
  if (arg) {
    const argText = looksLikePath(arg) ? basename(unquote(arg)) : unquote(arg);
    return `${prog} ${argText}`;
  }
  return prog;
}

/** Compact, tool-specific headline shown next to the emoji.
 *
 * `maxLen` is a soft character budget: callers pass a larger budget on
 * desktop and a smaller one on mobile so the same command surfaces more
 * detail where there's room. Defaults to the wide budget. */
export function toolHeadline(
  name: string | undefined | null,
  input: unknown,
  maxLen: number = HEADLINE_BUDGET_WIDE,
): string {
  const n = name || "tool";
  const summary = inputSummary(name, input).trim();

  switch (n) {
    case "bash":
    case "shell":
      return bashHeadline(summary, maxLen);
    case "patch":
      return summary ? basename(summary) : n;
    case "change_dir":
      return summary || n;
    case "screenshot":
    case "browser_take_screenshot":
    case "read_image":
    case "browser_navigate":
    case "keyword_search":
      return summary || n;
    default: {
      if (!summary) return n;
      const firstLine = summary.split(/\r?\n/)[0] || summary;
      return `${n}: ${firstLine}`;
    }
  }
}

// Pull the most-relevant single string out of a tool input payload.
function inputSummary(name: string | undefined | null, input: unknown): string {
  if (input == null) return "";
  if (typeof input === "string") return input;
  if (typeof input !== "object") return String(input);
  const o = input as Record<string, unknown>;
  const pick = (...keys: string[]): string => {
    for (const k of keys) {
      const v = o[k];
      if (typeof v === "string" && v.length > 0) return v;
    }
    return "";
  };
  switch (name) {
    case "bash":
    case "shell":
      return pick("command");
    case "patch":
    case "change_dir":
      return pick("path");
    case "screenshot":
    case "browser_take_screenshot":
      return pick("selector", "url");
    case "read_image":
      return pick("path", "url");
    case "browser_navigate":
      return pick("url");
    case "keyword_search":
    case "web_search":
      return pick("query");
    case "subagent":
      return pick("slug", "prompt");
    case "llm_one_shot": {
      const files = o.prompt_files;
      if (Array.isArray(files) && files.length > 0) return files.join(", ");
      if (typeof files === "string") return files;
      return pick("prompt");
    }
    case "output_iframe":
      return pick("title", "path");
    case "browser_eval":
      return pick("expression");
    case "browser_emulate":
      return pick("device", "media");
    case "browser_resize": {
      const w = o.width,
        h = o.height;
      if (typeof w === "number" && typeof h === "number") return `${w}x${h}`;
      return "";
    }
    case "browser_accessibility":
    case "browser_network":
    case "browser_profile":
      return pick("action");
    default: {
      const v = pick("command", "path", "url", "query", "prompt", "action");
      if (v) return v;
      try {
        return JSON.stringify(input);
      } catch {
        return "";
      }
    }
  }
}

/** Tools whose entire value is the inline rendering (diffs, images,
 *  iframes). These are NOT collapsed into pills; they keep the
 *  current full-bleed card so the user sees the diff / image
 *  without an extra tap.
 */
export function isAutoExpandTool(name: string | undefined | null, input?: unknown): boolean {
  // The umbrella "browser" tool multiplexes many actions; only its
  // screenshot action produces an inline image worth auto-expanding.
  if (name === "browser") {
    return browserAction(input) === "screenshot";
  }
  switch (name) {
    case "patch":
    case "screenshot":
    case "browser_take_screenshot":
    case "read_image":
    case "output_iframe":
      return true;
    default:
      return false;
  }
}
