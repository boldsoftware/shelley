// What the Shelley bookmarklet, running in someone else's page, and the
// Shelley window it opened say to each other over postMessage. Shelley treats
// everything from the page as untrusted data: text for the composer, or a
// recording's events. The one request it acts on is to start a recording,
// and only from a site the user allowed to, in Shelley.
import { clipSelection, collapse, type ReviewEvent } from "../vue/components/reviewCapture";

export const TAG = "shelley-bookmarklet";
// The bookmarklet opens Shelley at /new#bookmarklet=<PageContext as JSON>.
export const FRAGMENT = "bookmarklet=";

export interface PageContext {
  url: string;
  title: string;
  selection: string;
}

export interface PagePick {
  url: string;
  selector: string;
  html: string;
}

export type ShelleyMessage =
  | { tag: typeof TAG; type: "hello" }
  | { tag: typeof TAG; type: "trace"; on: true; id: number }
  | { tag: typeof TAG; type: "trace"; on: false };

export type PageMessage =
  | { tag: typeof TAG; type: "hi" }
  // Trace id began; its events' ms count from epoch, the page's Date.now().
  | { tag: typeof TAG; type: "trace"; id: number; epoch: number }
  | { tag: typeof TAG; type: "event"; event: ReviewEvent }
  | { tag: typeof TAG; type: "pick"; pick: PagePick }
  // Start recording; only Shelley's own controls stop (and send) it.
  | { tag: typeof TAG; type: "record" };

export const HTML_LIMIT = 2000;
const URL_LIMIT = 2000;
const SELECTOR_LIMIT = 500;
const TEXT_LIMIT = 4000;
const EVENT_TYPES = new Set([
  "view",
  "on_screen",
  "pointer",
  "pointer_leave",
  "selection",
  "click",
  "dialog",
  "page",
]);

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null;
const clip = (value: unknown, limit: number) =>
  typeof value === "string" ? value.slice(0, limit) : undefined;
function webURL(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  let url: URL;
  try {
    url = new URL(value.slice(0, 64 * URL_LIMIT));
  } catch {
    return undefined;
  }
  if (url.protocol !== "https:" && url.protocol !== "http:") return undefined;
  // A long query doesn't change which page it is.
  if (url.href.length > URL_LIMIT) {
    url.search = "";
    url.hash = "";
  }
  return url.href.length <= URL_LIMIT ? url.href : undefined;
}

export function parsePageContext(value: unknown): PageContext | null {
  if (!isRecord(value)) return null;
  const url = webURL(value.url);
  if (!url) return null;
  return {
    url,
    title: collapse(clip(value.title, TEXT_LIMIT), 200),
    selection: typeof value.selection === "string" ? clipSelection(value.selection) : "",
  };
}

function parseEvent(value: unknown): ReviewEvent | null {
  if (!isRecord(value)) return null;
  const { ms, type } = value;
  if (typeof ms !== "number" || !Number.isFinite(ms) || typeof type !== "string") return null;
  if (!EVENT_TYPES.has(type)) return null;
  const event: ReviewEvent = { ms, type };
  for (const key of ["where", "text", "mode", "state"] as const) {
    const text = clip(value[key], TEXT_LIMIT);
    if (text !== undefined) event[key] = text;
  }
  if (Array.isArray(value.items)) {
    event.items = value.items
      .filter((item): item is string => typeof item === "string")
      .slice(0, 8)
      .map((item) => collapse(item, 200));
  }
  if (typeof value.open === "boolean") event.open = value.open;
  return event;
}

export function parsePageMessage(data: unknown): PageMessage | null {
  if (!isRecord(data) || data.tag !== TAG) return null;
  switch (data.type) {
    case "hi":
    case "record":
      return { tag: TAG, type: data.type };
    case "trace":
      return typeof data.id === "number" && Number.isFinite(data.epoch)
        ? { tag: TAG, type: "trace", id: data.id, epoch: data.epoch as number }
        : null;
    case "event": {
      const event = parseEvent(data.event);
      return event && { tag: TAG, type: "event", event };
    }
    case "pick": {
      const pick = data.pick;
      if (!isRecord(pick)) return null;
      const url = webURL(pick.url);
      const selector =
        typeof pick.selector === "string" ? collapse(pick.selector, SELECTOR_LIMIT) : "";
      const html = clip(pick.html, HTML_LIMIT);
      return url && selector && html !== undefined
        ? { tag: TAG, type: "pick", pick: { url, selector, html } }
        : null;
    }
  }
  return null;
}

function fence(text: string, lang: string): string {
  let marker = "```";
  while (text.includes(marker)) marker += "`";
  return `${marker}${lang}\n${text}\n${marker}`;
}

// The composer text for the page the bookmarklet was clicked on.
export function pageText(page: PageContext): string {
  const lines = [`Page: ${page.title ? `${page.title} — ` : ""}${page.url}`];
  if (page.selection.trim()) lines.push(...page.selection.split("\n").map((line) => `> ${line}`));
  return lines.join("\n");
}

// The composer text for an element picked on the page.
export function pickText(pick: PagePick): string {
  return `Picked on ${pick.url}: \`${pick.selector}\`\n${fence(pick.html, "html")}`;
}
