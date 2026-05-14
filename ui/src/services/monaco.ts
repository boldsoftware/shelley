import type * as Monaco from "monaco-editor";

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
      getWorkerUrl: () => "/editor.worker.js",
    };
    (self as Window).MonacoEnvironment = monacoEnv;

    // Load Monaco CSS if not already loaded
    if (!document.querySelector('link[href="/monaco-editor.css"]')) {
      const link = document.createElement("link");
      link.rel = "stylesheet";
      link.href = "/monaco-editor.css";
      document.head.appendChild(link);
    }

    // Load Monaco from our local bundle (runtime URL, cast to proper types)
    // eslint-disable-next-line @typescript-eslint/ban-ts-comment
    // @ts-ignore - dynamic runtime URL import
    const monaco = (await import("/monaco-editor.js")) as typeof Monaco;
    monacoInstance = monaco;
    return monacoInstance;
  })();

  return monacoLoadPromise;
}

// Vim mode adapter (lazy-loaded so users without vim enabled don't pay for it).
let vimModulePromise: Promise<typeof import("monaco-vim")> | null = null;
export function loadMonacoVim(): Promise<typeof import("monaco-vim")> {
  if (!vimModulePromise) {
    vimModulePromise = import("monaco-vim");
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

// Register ex commands (:q, :wq, :x) and ZZ/ZQ key mappings that route to
// a caller-provided quit handler. monaco-vim's Vim object is global, so we
// install these handlers once and dispatch via a module-level callback.
// Only one editor's quit handler is active at a time (set/cleared by the
// useMonacoVim hook when it attaches/detaches a vim adapter).
let activeQuitHandler: ((opts: { save: boolean }) => void) | null = null;
let vimQuitInstalled = false;

export function setVimQuitHandler(handler: ((opts: { save: boolean }) => void) | null): void {
  activeQuitHandler = handler;
}

// Clear the active handler only if it still matches the caller's. This
// prevents an unmounting adapter from wiping out a handler that a later
// adapter has since installed (e.g. when both modals are mounted at once).
export function clearVimQuitHandlerIf(handler: (opts: { save: boolean }) => void): void {
  if (activeQuitHandler === handler) activeQuitHandler = null;
}

export async function ensureVimQuitCommands(): Promise<void> {
  if (vimQuitInstalled) return;
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
  vimQuitInstalled = true;
}

interface VimApi {
  defineEx?: (name: string, prefix: string, fn: (...args: unknown[]) => void) => void;
  defineAction?: (name: string, fn: (...args: unknown[]) => void) => void;
  mapCommand?: (
    keys: string,
    type: string,
    name: string,
    args: unknown,
    extra: { context?: string },
  ) => void;
}
