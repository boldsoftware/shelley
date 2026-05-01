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
