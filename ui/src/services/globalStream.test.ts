// globalStream.test.ts — exercises the reconnect/liveness logic in
// connectGlobalStream without a real browser. We stub EventSource, the DOM
// event targets (document/window), and the wall clock so we can drive
// background-tab scenarios deterministically.
//
// Self-executing on import (see scripts/run-tests.mjs).

export {};

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(`Assertion failed: ${message}`);
}

async function run(name: string, fn: () => void | Promise<void>): Promise<void> {
  try {
    await fn();
    console.log(`\u2713 ${name}`);
  } catch (err) {
    console.error(`\u2717 ${name}`);
    throw err;
  }
}

// ---- Fakes -----------------------------------------------------------------

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSED = 2;

  url: string;
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }
  close() {
    this.closed = true;
    this.readyState = 2;
  }
  // Test helpers.
  emitOpen() {
    this.readyState = 1;
    this.onopen?.();
  }
  emitMessage(data: unknown) {
    this.readyState = 1;
    this.onmessage?.({ data: JSON.stringify(data) });
  }
  emitError() {
    // EventSource sets readyState to CONNECTING (0) while it retries, or
    // CLOSED (2) on a fatal error. The client treats either as down.
    this.readyState = 0;
    this.onerror?.();
  }
}

type Handler = () => void;

class FakeEventTarget {
  handlers = new Map<string, Set<Handler>>();
  addEventListener(type: string, fn: Handler) {
    if (!this.handlers.has(type)) this.handlers.set(type, new Set());
    this.handlers.get(type)!.add(fn);
  }
  removeEventListener(type: string, fn: Handler) {
    this.handlers.get(type)?.delete(fn);
  }
  dispatch(type: string) {
    for (const fn of this.handlers.get(type) ?? []) fn();
  }
}

// Controllable clock + timers so we never sleep in tests.
let now = 1_000_000;
let timerSeq = 1;
interface Timer {
  id: number;
  fireAt: number;
  fn: () => void;
}
let timers: Timer[] = [];

function fakeSetTimeout(fn: () => void, delay: number): number {
  const id = timerSeq++;
  timers.push({ id, fireAt: now + delay, fn });
  return id;
}
function fakeClearTimeout(id: number): void {
  timers = timers.filter((t) => t.id !== id);
}
// Advance the clock by ms, firing any timers due along the way. Timers
// scheduled while advancing are honored if they fall within the window.
function advance(ms: number): void {
  const target = now + ms;
  for (;;) {
    const due = timers.filter((t) => t.fireAt <= target).sort((a, b) => a.fireAt - b.fireAt);
    if (due.length === 0) break;
    const next = due[0];
    timers = timers.filter((t) => t.id !== next.id);
    now = next.fireAt;
    next.fn();
  }
  now = target;
}

const fakeDocument = new FakeEventTarget() as FakeEventTarget & {
  visibilityState: string;
};
fakeDocument.visibilityState = "visible";
const fakeWindow = new FakeEventTarget() as FakeEventTarget & {
  setTimeout: typeof fakeSetTimeout;
  clearTimeout: typeof fakeClearTimeout;
};
fakeWindow.setTimeout = fakeSetTimeout;
fakeWindow.clearTimeout = fakeClearTimeout;

// Install globals before importing the module under test.
const g = globalThis as unknown as Record<string, unknown>;
g.EventSource = FakeEventSource;
g.document = fakeDocument;
g.window = fakeWindow;
const realDateNow = Date.now;
Date.now = () => now;

// Reset shared state between scenarios.
function reset(): void {
  FakeEventSource.instances = [];
  timers = [];
  timerSeq = 1;
  now = 1_000_000;
  fakeDocument.visibilityState = "visible";
  fakeDocument.handlers.clear();
  fakeWindow.handlers.clear();
}

// ---- Module under test -----------------------------------------------------

const { connectGlobalStream } = await import("./globalStream");
const { messageStore } = await import("./messageStore");

// Spy on the two reconnect side effects without touching IndexedDB.
let markAllStaleCalls = 0;
messageStore.markAllStale = () => {
  markAllStaleCalls += 1;
};

function newStream() {
  let reconnects = 0;
  const statuses: string[] = [];
  const handle = connectGlobalStream({
    getHash: () => "hash0",
    onListPatch: () => {},
    onStatusChange: (s) => statuses.push(s),
    onReconnect: () => {
      reconnects += 1;
    },
  });
  return {
    handle,
    statuses,
    get reconnects() {
      return reconnects;
    },
  };
}

function latest(): FakeEventSource {
  return FakeEventSource.instances[FakeEventSource.instances.length - 1];
}

// ---- Scenarios -------------------------------------------------------------

await run("connects on creation", () => {
  reset();
  markAllStaleCalls = 0;
  const s = newStream();
  assert(FakeEventSource.instances.length === 1, "opened one EventSource");
  latest().emitOpen();
  // First connect must NOT trigger a stale-backfill.
  assert(markAllStaleCalls === 0, "no markAllStale on first connect");
  assert(s.reconnects === 0, "no onReconnect on first connect");
  s.handle.close();
});

await run("routes text and thinking deltas separately", () => {
  reset();
  const id = "stream-deltas";
  messageStore.resetTransient(id);
  const s = newStream();
  latest().emitOpen();
  latest().emitMessage({
    conversation_id: id,
    stream_delta: { type: "thinking", text: "consider", index: 0, seq: 1 },
  });
  latest().emitMessage({
    conversation_id: id,
    stream_delta: { type: "text", text: "answer", index: 1, seq: 2 },
  });
  const transient = messageStore.getTransient(id);
  assert(transient.streamingThinking === "consider", "thinking delta routed to thinking");
  assert(transient.streamingText === "answer", "text delta routed to text");
  s.handle.close();
});

await run("foreground resume reconnects a silent (zombie) connection", () => {
  reset();
  markAllStaleCalls = 0;
  const s = newStream();
  latest().emitOpen();
  const zombie = latest();

  // Tab goes to the background. No frames arrive; the socket is silently
  // dead but still reports OPEN (readyState 1). The heartbeat watchdog's
  // setTimeout is throttled/frozen while hidden, so advancing the clock
  // WITHOUT firing it models the real browser behavior: we manually keep
  // the watchdog from firing by NOT advancing timers here.
  fakeDocument.visibilityState = "hidden";
  fakeDocument.dispatch("visibilitychange");
  now += 120000; // 2 minutes of wall-clock pass with no frames

  // User returns. readyState is still OPEN (zombie), but >35s since the last
  // frame, so we must force a reconnect.
  assert(zombie.readyState === 1, "zombie still reports OPEN");
  fakeDocument.visibilityState = "visible";
  fakeDocument.dispatch("visibilitychange");

  assert(zombie.closed, "zombie connection closed on resume");
  assert(FakeEventSource.instances.length === 2, "opened a fresh EventSource");
  latest().emitOpen();
  assert(markAllStaleCalls === 1, "markAllStale fired after resume reconnect");
  assert(s.reconnects === 1, "onReconnect fired after resume reconnect");
  s.handle.close();
});

await run("foreground resume leaves a live connection alone", () => {
  reset();
  markAllStaleCalls = 0;
  const s = newStream();
  latest().emitOpen();
  const live = latest();

  fakeDocument.visibilityState = "hidden";
  fakeDocument.dispatch("visibilitychange");
  now += 10000; // 10s in background
  live.emitMessage({ heartbeat: true }); // a frame arrived recently
  now += 2000;

  fakeDocument.visibilityState = "visible";
  fakeDocument.dispatch("visibilitychange");
  // Last frame was 2s ago (< 35s): no reconnect.
  assert(!live.closed, "live connection not torn down");
  assert(FakeEventSource.instances.length === 1, "no extra EventSource opened");
  assert(markAllStaleCalls === 0, "no stale backfill for a live connection");
  s.handle.close();
});

await run("pageshow (bfcache restore) reconnects a stale connection", () => {
  reset();
  markAllStaleCalls = 0;
  const s = newStream();
  latest().emitOpen();
  const old = latest();

  now += 120000; // long bfcache nap, no visibilitychange fired
  fakeWindow.dispatch("pageshow");

  assert(old.closed, "stale connection closed on pageshow");
  assert(FakeEventSource.instances.length === 2, "reconnected on pageshow");
  latest().emitOpen();
  assert(markAllStaleCalls === 1, "markAllStale fired after pageshow reconnect");
  assert(s.reconnects === 1, "onReconnect fired after pageshow reconnect");
  s.handle.close();
});

await run("resume during a CONNECTING attempt does not tear it down", () => {
  reset();
  markAllStaleCalls = 0;
  const s = newStream();
  // The very first EventSource is created but has not opened yet
  // (readyState CONNECTING). A resume signal arriving now must leave the
  // in-progress handshake alone rather than churning it.
  const connecting = latest();
  assert(connecting.readyState === 0, "socket is CONNECTING");
  now += 120000; // even with an old wall clock
  fakeDocument.visibilityState = "visible";
  fakeDocument.dispatch("visibilitychange");
  fakeWindow.dispatch("pageshow");
  fakeWindow.dispatch("online");
  assert(!connecting.closed, "CONNECTING socket left intact");
  assert(FakeEventSource.instances.length === 1, "no extra EventSource opened");
  s.handle.close();
});

await run("heartbeat watchdog reconnects a foreground zombie", () => {
  reset();
  markAllStaleCalls = 0;
  const s = newStream();
  latest().emitOpen();
  const zombie = latest();

  // Foreground tab: the 60s watchdog timer is NOT throttled, so advancing
  // the clock past 60s fires it even though no error/close ever surfaced.
  advance(61000);
  assert(zombie.closed, "zombie closed by watchdog");
  assert(FakeEventSource.instances.length === 2, "watchdog reconnected");
  latest().emitOpen();
  assert(markAllStaleCalls === 1, "markAllStale fired after watchdog reconnect");
  assert(s.reconnects === 1, "onReconnect fired after watchdog reconnect");
  s.handle.close();
});

await run("error backoff reconnects and then recovers", () => {
  reset();
  markAllStaleCalls = 0;
  const s = newStream();
  latest().emitOpen();

  // Connection drops. First retry is at 1s.
  latest().emitError();
  assert(s.statuses.includes("reconnecting"), "status went reconnecting");
  advance(1000);
  assert(FakeEventSource.instances.length === 2, "retried after 1s");
  latest().emitOpen();
  assert(markAllStaleCalls === 1, "markAllStale fired after backoff recovery");
  assert(s.reconnects === 1, "onReconnect fired after backoff recovery");
  s.handle.close();
});

await run("online event recovers from the long backoff fast", () => {
  reset();
  markAllStaleCalls = 0;
  const s = newStream();
  latest().emitOpen();

  // Burn through the backoff schedule (1s, 2s, 5s) into the 30s tier.
  latest().emitError(); // attempt 1
  advance(1000);
  latest().emitError(); // attempt 2
  advance(2000);
  latest().emitError(); // attempt 3
  advance(5000);
  latest().emitError(); // attempt 4 -> 30s tier, status disconnected
  assert(s.statuses.includes("disconnected"), "status went disconnected");
  const beforeOnline = FakeEventSource.instances.length;

  // Network returns: reconnect immediately rather than waiting out the 30s.
  fakeWindow.dispatch("online");
  assert(
    FakeEventSource.instances.length === beforeOnline + 1,
    "online forced an immediate reconnect",
  );
  latest().emitOpen();
  assert(s.statuses[s.statuses.length - 1] === "connected", "recovered to connected");
  s.handle.close();
});

await run("accept-then-drop cycles keep escalating backoff", () => {
  reset();
  markAllStaleCalls = 0;
  const s = newStream();
  latest().emitOpen();

  // The server accepts each stream and drops it moments later (e.g.
  // subscriber-queue overflow under a stream flood). Pre-fix, every
  // short-lived open reset the attempt counter, so the client hammered the
  // server at the minimum 1s delay forever — replaying the full list
  // snapshot and a REST backfill on every cycle. Each connect→quick-drop
  // must instead count as a consecutive failure: 1s→2s→5s→30s.
  latest().emitError(); // died instantly: attempt 1
  advance(1000);
  assert(FakeEventSource.instances.length === 2, "retried after 1s");
  latest().emitOpen(); // accepted...
  advance(3000); // ...but only lived 3s (< stability window)
  latest().emitError(); // attempt 2
  advance(2000);
  assert(FakeEventSource.instances.length === 3, "second retry waited 2s");
  latest().emitOpen();
  advance(3000);
  latest().emitError(); // attempt 3
  advance(2000);
  assert(FakeEventSource.instances.length === 3, "third retry not due before 5s");
  advance(3000);
  assert(FakeEventSource.instances.length === 4, "third retry waited 5s");
  latest().emitOpen();
  advance(3000);
  latest().emitError(); // attempt 4 -> 30s tier
  assert(s.statuses.includes("disconnected"), "status reflects the persistent failure");
  advance(29000);
  assert(FakeEventSource.instances.length === 4, "fourth retry not due before 30s");
  advance(1000);
  assert(FakeEventSource.instances.length === 5, "fourth retry waited 30s");

  // A connection that survives the stability window earns a fresh ladder:
  // the next drop retries at 1s again.
  latest().emitOpen();
  advance(31000);
  latest().emitError();
  advance(1000);
  assert(FakeEventSource.instances.length === 6, "stable connection reset backoff to 1s");
  s.handle.close();
});

await run("close() removes listeners and stops reconnecting", () => {
  reset();
  const s = newStream();
  latest().emitOpen();
  s.handle.close();
  const count = FakeEventSource.instances.length;
  // Post-close events must be inert.
  fakeDocument.visibilityState = "visible";
  fakeDocument.dispatch("visibilitychange");
  fakeWindow.dispatch("pageshow");
  fakeWindow.dispatch("online");
  advance(120000);
  assert(FakeEventSource.instances.length === count, "no reconnects after close()");
});

Date.now = realDateNow;
console.log("\nglobalStream: all scenarios passed");
