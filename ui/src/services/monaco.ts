import type * as Monaco from "monaco-editor";
import { hardWrapLines, DEFAULT_TEXT_WIDTH } from "./vimHardWrap";

// The Monaco bundle's URLs, which carry content hashes (scripts/build.js).
declare const __MONACO_URL__: string;
declare const __MONACO_CSS_URL__: string;
declare const __EDITOR_WORKER_URL__: string;

// Global Monaco instance - loaded lazily, shared across components
let monacoInstance: typeof Monaco | null = null;
let monacoLoadPromise: Promise<typeof Monaco> | null = null;

export function loadMonaco(): Promise<typeof Monaco> {
  if (monacoInstance) {
    return Promise.resolve(monacoInstance);
  }
  if (monacoLoadPromise) {
    return monacoLoadPromise;
  }

  monacoLoadPromise = (async () => {
    // Configure Monaco environment for web workers before importing
    const monacoEnv: Monaco.Environment = {
      getWorkerUrl: () => __EDITOR_WORKER_URL__,
    };
    (self as Window).MonacoEnvironment = monacoEnv;

    // Load Monaco CSS if not already loaded
    if (!document.querySelector(`link[href="${__MONACO_CSS_URL__}"]`)) {
      const link = document.createElement("link");
      link.rel = "stylesheet";
      link.href = __MONACO_CSS_URL__;
      document.head.appendChild(link);
    }

    const monaco = (await import(__MONACO_URL__)) as typeof Monaco;
    monacoInstance = monaco;
    return monacoInstance;
  })();

  return monacoLoadPromise;
}

// Vim mode adapter. It ships inside the Monaco bundle (see
// src/monaco-bundle-entry.js), so it loads with Monaco, not with main.js.
let vimModulePromise: Promise<typeof import("monaco-vim")> | null = null;
export function loadMonacoVim(): Promise<typeof import("monaco-vim")> {
  if (!vimModulePromise) {
    vimModulePromise = loadMonaco().then((m) => m as unknown as typeof import("monaco-vim"));
  }
  return vimModulePromise;
}

// localStorage helpers for the vim toggle. We persist a single global flag
// that applies to every Monaco view (AGENTS.md editor + diff viewer).
const VIM_STORAGE_KEY = "shelley.monacoVim";
export function getVimModeEnabled(): boolean {
  try {
    return localStorage.getItem(VIM_STORAGE_KEY) === "1";
  } catch {
    return false;
  }
}
export function setVimModeEnabled(enabled: boolean): void {
  try {
    if (enabled) localStorage.setItem(VIM_STORAGE_KEY, "1");
    else localStorage.removeItem(VIM_STORAGE_KEY);
  } catch {
    // ignore
  }
}

// Register our additions to monaco-vim's global Vim object: ex commands
// (:q, :wq, :x) and ZZ/ZQ key mappings that route to a caller-provided quit
// handler, plus the gq/gw formatting operators that upstream lacks. The Vim
// object is global, so we install these once and dispatch via a
// module-level callback. Only one editor's quit handler is active at a time
// (set/cleared by the useMonacoVim hook when it attaches/detaches a vim
// adapter).
let activeQuitHandler: ((opts: { save: boolean }) => void) | null = null;
let vimExtensionsInstalled = false;

export function setVimQuitHandler(handler: ((opts: { save: boolean }) => void) | null): void {
  activeQuitHandler = handler;
}

// Clear the active handler only if it still matches the caller's. This
// prevents an unmounting adapter from wiping out a handler that a later
// adapter has since installed (e.g. when both modals are mounted at once).
export function clearVimQuitHandlerIf(handler: (opts: { save: boolean }) => void): void {
  if (activeQuitHandler === handler) activeQuitHandler = null;
}

export async function ensureVimExtensions(): Promise<void> {
  if (vimExtensionsInstalled) return;
  const mod = await loadMonacoVim();
  // CMAdapter (VimMode) exposes the underlying Vim API as a static property.
  // It's not declared in monaco-vim's types, so cast through unknown.
  const Vim = (mod.VimMode as unknown as { Vim?: VimApi }).Vim;
  if (!Vim) return;
  const quit = (save: boolean) => () => activeQuitHandler?.({ save });
  Vim.defineEx?.("quit", "q", quit(false));
  Vim.defineEx?.("wq", "wq", quit(true));
  Vim.defineEx?.("xit", "x", quit(true));
  // ZZ = save+quit, ZQ = quit without saving. Use `action` mappings backed
  // by defineAction so the keys work in normal mode without conflicting
  // with existing motions.
  Vim.defineAction?.("shelleyQuit", () => activeQuitHandler?.({ save: false }));
  Vim.defineAction?.("shelleyQuitSave", () => activeQuitHandler?.({ save: true }));
  Vim.mapCommand?.("ZQ", "action", "shelleyQuit", undefined, { context: "normal" });
  Vim.mapCommand?.("ZZ", "action", "shelleyQuitSave", undefined, { context: "normal" });
  installVimFormatOperators(Vim);
  vimExtensionsInstalled = true;
}

// gq / gw: reflow text to `textwidth` (default 79, settable via
// `:set textwidth=N` / `:set tw=N`). monaco-vim's CodeMirror keymap has no
// hardwrap support, so `gqip`, `gqq`, `gqj`, visual `gq`, etc. were all
// silent no-ops. We register a linewise operator that replaces the covered
// lines with hardWrapLines() output. gq leaves the cursor on the first
// non-blank of the last formatted line (as vim does); gw restores it to
// where it was before the operator ran. The reflow is language-aware
// (comment leaders in code, list items / blockquotes in prose); see
// vimHardWrap.ts. `:set joinspaces` / `:set js` puts two spaces after
// sentence-ending punctuation when joining, as in vim.
function installVimFormatOperators(Vim: VimApi): void {
  Vim.defineOption?.("textwidth", DEFAULT_TEXT_WIDTH, "number", ["tw"]);
  Vim.defineOption?.("joinspaces", false, "boolean", ["js"]);

  Vim.defineOperator?.("shelleyHardWrap", (cm, args, ranges, oldAnchor) => {
    // Range handling mirrors upstream's `indent` operator: linewise ranges
    // end at (nextLine, 0), so step back one line; visual-block mode gives
    // one range per line instead.
    const startLine = ranges[0].anchor.line;
    let endLine: number;
    if (cm.state.vim?.visualBlock) {
      endLine = ranges[ranges.length - 1].anchor.line;
    } else {
      endLine = ranges[0].head.line;
      if (args.linewise && endLine > startLine) endLine--;
    }
    endLine = Math.min(endLine, cm.lastLine());

    const lines: string[] = [];
    for (let i = startLine; i <= endLine; i++) lines.push(cm.getLine(i));

    // `:set tw=N` stores the raw string; vim treats tw=0 as "use 79".
    const raw = Vim.getOption?.("textwidth", cm);
    const n = typeof raw === "number" ? raw : parseInt(String(raw), 10);
    const width = Number.isFinite(n) && n > 0 ? n : DEFAULT_TEXT_WIDTH;
    const model = cm.editor.getModel();
    const wrapped = hardWrapLines(lines, width, {
      languageId: model?.getLanguageId(),
      tabstop: model?.getOptions().tabSize,
      joinspaces: Vim.getOption?.("joinspaces", cm) === true,
    });

    const lastLen = cm.getLine(endLine).length;
    cm.pushUndoStop();
    cm.replaceRange(wrapped.join("\n"), { line: startLine, ch: 0 }, { line: endLine, ch: lastLen });
    cm.pushUndoStop();

    if (args.keepCursor) {
      const line = Math.min(oldAnchor.line, cm.lastLine());
      const ch = Math.min(oldAnchor.ch, Math.max(0, cm.getLine(line).length - 1));
      return { line, ch };
    }
    const newLast = startLine + Math.max(0, wrapped.length - 1);
    const firstNonWs = cm.getLine(newLast).search(/\S/);
    return { line: newLast, ch: firstNonWs === -1 ? 0 : firstNonWs };
  });

  Vim.mapCommand?.("gq", "operator", "shelleyHardWrap", { linewise: true }, {});
  Vim.mapCommand?.("gw", "operator", "shelleyHardWrap", { linewise: true, keepCursor: true }, {});
}

interface VimPos {
  line: number;
  ch: number;
}

// Subset of monaco-vim's CodeMirror-shaped adapter that the operator touches.
interface VimCm {
  state: { vim?: { visualBlock?: boolean } };
  editor: Monaco.editor.IStandaloneCodeEditor;
  getLine(line: number): string;
  lastLine(): number;
  replaceRange(text: string, from: VimPos, to: VimPos): void;
  pushUndoStop(): void;
}

interface VimApi {
  defineEx?: (name: string, prefix: string, fn: (...args: unknown[]) => void) => void;
  defineAction?: (name: string, fn: (...args: unknown[]) => void) => void;
  defineOperator?: (
    name: string,
    fn: (
      cm: VimCm,
      args: { linewise?: boolean; keepCursor?: boolean },
      ranges: Array<{ anchor: VimPos; head: VimPos }>,
      oldAnchor: VimPos,
      newHead: VimPos,
    ) => VimPos | void,
  ) => void;
  defineOption?: (name: string, defaultValue: unknown, type: string, aliases?: string[]) => void;
  getOption?: (name: string, cm?: unknown) => unknown;
  mapCommand?: (
    keys: string,
    type: string,
    name: string,
    args: unknown,
    extra: { context?: string },
  ) => void;
}
