// Framework-agnostic configuration + singleton accessor for the @pierre/diffs
// syntax-highlighting worker pool.
//
// React wires this up via <WorkerPoolContextProvider> in App.tsx (which calls
// getOrCreateWorkerPoolSingleton under the hood). The Vue world has no such
// provider, so its PatchTool drives the FileDiff renderer with the same shared
// singleton directly (see vue/composables/fileDiffInstance.ts). Keeping the
// pool/highlighter options here means both worlds preload the exact same set
// of languages and run tokenization off the main thread.
import type { SupportedLanguages } from "@pierre/diffs";
import { getOrCreateWorkerPoolSingleton, type WorkerPoolManager } from "@pierre/diffs/worker";

// The worker bundle's URL, which carries a content hash (scripts/build.js).
declare const __DIFFS_WORKER_URL__: string;

// Workers run shiki/TextMate tokenization off the main thread so large
// conversations (hundreds of diffs) don't freeze the UI while highlighting.
export const diffsPoolOptions = {
  workerFactory: () => new Worker(__DIFFS_WORKER_URL__),
};

// Languages to eagerly preload in the highlighter so the common cases render
// instantly. This is a subset of PatchTool's langMap, not the full set:
// SupportedLanguages accepts any grammar name and @pierre/diffs lazy-loads any
// language not listed here on first use (a small one-time cost), so rarer
// extensions (lua, perl, dockerfile, vue, svelte, …) don't need to bloat every
// worker's startup. Carried over verbatim from the original React App.tsx
// config so both worlds share identical preload behavior.
export const diffsHighlighterOptions = {
  langs: [
    "typescript",
    "tsx",
    "javascript",
    "jsx",
    "python",
    "ruby",
    "go",
    "rust",
    "java",
    "c",
    "cpp",
    "csharp",
    "php",
    "swift",
    "kotlin",
    "scala",
    "bash",
    "sql",
    "html",
    "css",
    "scss",
    "json",
    "xml",
    "yaml",
    "toml",
    "markdown",
  ] as SupportedLanguages[],
};

// getDiffsWorkerPool returns the process-wide WorkerPoolManager singleton,
// creating it (and its workers) on first use. Returns undefined in non-browser
// contexts (no window), matching React's provider behavior.
export function getDiffsWorkerPool(): WorkerPoolManager | undefined {
  if (typeof window === "undefined") return undefined;
  return getOrCreateWorkerPoolSingleton({
    poolOptions: diffsPoolOptions,
    highlighterOptions: diffsHighlighterOptions,
  });
}
