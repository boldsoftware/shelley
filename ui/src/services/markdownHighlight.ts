// Fenced-code syntax highlighting runs entirely in a dedicated worker. The
// service owns its one worker, de-duplicates requests, and bounds the source
// cache because streaming markdown can produce many short-lived revisions.
import { SHIKI_LANGUAGE_BY_LABEL } from "../generated-shiki-language-metadata";

export { SHIKI_LANGUAGE_BY_LABEL };

export interface HighlightToken {
  content: string;
  light: string;
  dark: string;
  fontStyle: number;
}

export type HighlightLines = HighlightToken[][];

export type HighlightResult = { kind: "highlighted"; lines: HighlightLines } | { kind: "unknown" };

interface HighlightRequest {
  id: number;
  language: string;
  source: string;
}

type HighlightResponse =
  | { id: number; kind: "highlighted"; lines: HighlightLines }
  | { id: number; kind: "unknown" }
  | { id: number; kind: "error"; error: string };

interface PendingRequest {
  resolve: (result: HighlightResult) => void;
  reject: (reason: Error) => void;
}

const MAX_CACHE_ENTRIES = 128;
const pending = new Map<number, PendingRequest>();
const cache = new Map<string, Promise<HighlightResult>>();
let worker: Worker | undefined;
let nextRequestId = 1;

export function normalizeCodeLanguage(language: string | undefined): string | undefined {
  return language ? SHIKI_LANGUAGE_BY_LABEL[language.trim().toLowerCase()] : undefined;
}

function cacheKey(language: string, source: string): string {
  return `${language}\0${source}`;
}

function getWorker(): Worker {
  if (worker) return worker;

  worker = new Worker("/markdown-highlight-worker.js");
  worker.addEventListener("message", ({ data }: MessageEvent<HighlightResponse>) => {
    const request = pending.get(data.id);
    if (!request) return;
    pending.delete(data.id);
    if (data.kind === "error") {
      request.reject(new Error(data.error));
      return;
    }
    if (data.kind === "unknown") {
      request.resolve({ kind: "unknown" });
      return;
    }
    request.resolve({ kind: "highlighted", lines: data.lines });
  });
  worker.addEventListener("error", (event) => {
    const error = new Error(`Syntax highlighter worker failed: ${event.message}`);
    for (const request of pending.values()) request.reject(error);
    pending.clear();
    worker = undefined;
  });
  return worker;
}

function requestHighlight(language: string, source: string): Promise<HighlightResult> {
  const id = nextRequestId++;
  return new Promise<HighlightResult>((resolve, reject) => {
    pending.set(id, { resolve, reject });
    getWorker().postMessage({ id, language, source } satisfies HighlightRequest);
  });
}

// highlightCode returns a typed worker result. Callers normally pass a
// normalized bundled label, while an unexpected label still resolves as
// "unknown" instead of surfacing a worker error.
export function highlightCode(language: string, source: string): Promise<HighlightResult> {
  const key = cacheKey(language, source);
  const cached = cache.get(key);
  if (cached) return cached;

  const result = requestHighlight(language, source).catch((error: unknown) => {
    cache.delete(key);
    throw error;
  });
  cache.set(key, result);
  if (cache.size > MAX_CACHE_ENTRIES) cache.delete(cache.keys().next().value as string);
  return result;
}
