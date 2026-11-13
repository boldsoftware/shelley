// The Shelley half of the bookmarklet: a chat opened by the bookmarklet
// (/new#bookmarklet=...) starts with the page in the composer and, while its
// opener is linked, takes picked elements into the composer and the page's
// pointer trace into recordings. Everything from the page is data, save one
// request: to start a recording, which only Shelley's controls stop and send.
// Any site can run the bookmarklet's code or fake it, so Shelley starts one
// only for sites the user allowed to, by the origin the browser vouches for.
// Allowing a site trusts every script on it, bookmarklet or not, to start
// recordings. Behind the "bookmarklet" feature flag.
import { ref, shallowRef } from "vue";
import type { ReviewEvent } from "../components/reviewCapture";
import {
  FRAGMENT,
  TAG,
  pageText,
  parsePageContext,
  parsePageMessage,
  pickText,
  type PageContext,
  type ShelleyMessage,
} from "../../bookmarklet/protocol";

const STORAGE_KEY = "shelley-bookmarklet-page";
// Origins the user let start recordings.
const SITES_KEY = "shelley-bookmarklet-sites";

export const bookmarkletEnabled = window.__SHELLEY_INIT__?.bookmarklet === true;

export const bookmarkletPage = shallowRef<PageContext | null>(null);
export const bookmarkletLinked = ref(false);
export const bookmarkletTracing = ref(false);
// Text for the composer, oldest first; ChatInterface drains it.
export const bookmarkletInserts = ref<string[]>([]);
// Counts the page's requests to start a recording; App watches it.
export const bookmarkletRecordRequests = ref(0);
export const bookmarkletSites = ref<string[]>([]);
// Whether Shelley asks the user to let the page's site start recordings.
export const bookmarkletAsking = ref(false);
// Allow ignores clicks until a moment after it appears, Shelley's window
// gains focus, or the user last clicked, so a page can't slip it under a
// click meant for the page.
export const bookmarkletAllowReady = ref(false);
// The page's origin, which an Allow grants.
export const bookmarkletOrigin = ref("");

// A long recording's trace is still small; this bounds a page that floods.
const EVENT_LIMIT = 10000;

const ALLOW_DELAY_MS = 1000;

let allowTimer = 0;
let traces = 0;
let trace: { id: number; epoch: number | null; events: ReviewEvent[] } | null = null;

function send(message: ShelleyMessage) {
  window.opener?.postMessage(message, bookmarkletOrigin.value);
}

function onMessage(event: MessageEvent) {
  if (event.source !== window.opener || event.origin !== bookmarkletOrigin.value) return;
  const message = parsePageMessage(event.data);
  if (!message) return;
  bookmarkletLinked.value = true;
  if (message.type === "pick") {
    bookmarkletInserts.value = [...bookmarkletInserts.value, pickText(message.pick)];
  } else if (message.type === "record") {
    if (bookmarkletSites.value.includes(bookmarkletOrigin.value)) bookmarkletRecordRequests.value++;
    else if (!bookmarkletAsking.value) {
      bookmarkletAsking.value = true;
      delayAllow();
    }
  } else if (message.type === "trace" && trace && message.id === trace.id) {
    trace.epoch = message.epoch;
    bookmarkletTracing.value = true;
  } else if (message.type === "event" && trace?.epoch != null) {
    if (trace.events.length < EVENT_LIMIT) trace.events.push(message.event);
  }
}

function storedSites(): string[] {
  return JSON.parse(localStorage.getItem(SITES_KEY) ?? "[]");
}

function storeSites(sites: string[]) {
  localStorage.setItem(SITES_KEY, JSON.stringify(sites));
  bookmarkletSites.value = sites;
}

// Answers bookmarkletAsking: lets the page's site start recordings, starting
// one now, or not.
export function answerBookmarkletAsk(allow: boolean) {
  if (allow && !bookmarkletAllowReady.value) return;
  bookmarkletAsking.value = false;
  if (!allow) return;
  if (!bookmarkletSites.value.includes(bookmarkletOrigin.value))
    storeSites([...bookmarkletSites.value, bookmarkletOrigin.value]);
  bookmarkletRecordRequests.value++;
}

function delayAllow() {
  bookmarkletAllowReady.value = false;
  clearTimeout(allowTimer);
  allowTimer = window.setTimeout(() => (bookmarkletAllowReady.value = true), ALLOW_DELAY_MS);
}

export function forgetBookmarkletSite(site: string) {
  storeSites(bookmarkletSites.value.filter((s) => s !== site));
}

// Call once before mounting the app.
export function initBookmarklet() {
  if (!bookmarkletEnabled) return;
  bookmarkletSites.value = storedSites();
  // Other Shelley windows may allow or forget sites.
  window.addEventListener("storage", (event) => {
    if (event.key === SITES_KEY || event.key === null) bookmarkletSites.value = storedSites();
  });
  const hash = location.hash.slice(1);
  let page: PageContext | null = null;
  if (hash.startsWith(FRAGMENT)) {
    try {
      page = parsePageContext(JSON.parse(decodeURIComponent(hash.slice(FRAGMENT.length))));
    } catch {
      page = null;
    }
    history.replaceState(history.state, "", location.pathname + location.search);
    if (page) {
      sessionStorage.setItem(STORAGE_KEY, JSON.stringify(page));
      bookmarkletInserts.value = [pageText(page)];
    }
  } else {
    // A reload keeps the link, but not a second copy of the page text.
    const stored = sessionStorage.getItem(STORAGE_KEY);
    page = stored ? parsePageContext(JSON.parse(stored)) : null;
  }
  bookmarkletPage.value = page;
  if (!page || !window.opener) return;
  bookmarkletOrigin.value = new URL(page.url).origin;
  window.addEventListener("message", onMessage);
  window.addEventListener("focus", () => {
    if (bookmarkletAsking.value) delayAllow();
  });
  window.addEventListener(
    "pointerdown",
    () => {
      if (bookmarkletAsking.value && !bookmarkletAllowReady.value) delayAllow();
    },
    true,
  );
  send({ tag: TAG, type: "hello" });
}

export interface PageTrace {
  // The trace as a review recording (.review.json) aligned to a recording
  // that started at startedAt (Date.now()) and lasted durationMs; null when
  // the page never answered.
  stop(startedAt: number, durationMs: number): Blob | null;
  cancel(): void;
}

// Asks the linked page to report its pointer, clicks, and selection while a
// recording runs. null when no page is linked.
export function startPageTrace(): PageTrace | null {
  const page = bookmarkletPage.value;
  if (!page || !bookmarkletLinked.value || window.opener?.closed !== false) return null;
  trace = { id: ++traces, epoch: null, events: [] };
  const own = trace;
  send({ tag: TAG, type: "trace", on: true, id: own.id });
  const end = () => {
    if (trace !== own) return;
    trace = null;
    bookmarkletTracing.value = false;
    send({ tag: TAG, type: "trace", on: false });
  };
  return {
    stop(startedAt, durationMs) {
      end();
      if (own.epoch === null) return null;
      const shift = own.epoch - startedAt;
      const events = own.events
        .map((event) => ({ ...event, ms: Math.max(0, event.ms + shift) }))
        .filter((event) => event.ms <= durationMs);
      events.push({ ms: durationMs, type: "stop" });
      const review = {
        version: 1,
        source: "web-page",
        url: page.url,
        title: page.title,
        started_at: new Date(startedAt).toISOString(),
        duration_ms: durationMs,
        events,
      };
      return new Blob([JSON.stringify(review)], { type: "application/json" });
    },
    cancel: end,
  };
}
