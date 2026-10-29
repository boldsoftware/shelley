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
let reloadCalls = 0;
const fakeWindow = new FakeEventTarget() as FakeEventTarget & {
  setTimeout: typeof fakeSetTimeout;
  clearTimeout: typeof fakeClearTimeout;
  location: { reload: () => void };
  sessionStorage: Map<string, string> & {
    getItem: (k: string) => string | null;
    setItem: (k: string, v: string) => void;
  };
  __SHELLEY_INIT__?: { ui_build?: string };
};
fakeWindow.setTimeout = fakeSetTimeout;
fakeWindow.clearTimeout = fakeClearTimeout;
fakeWindow.location = { reload: () => (reloadCalls += 1) };
const fakeSession = new Map<string, string>();
fakeWindow.sessionStorage = Object.assign(fakeSession, {
  getItem: (k: string) => fakeSession.get(k) ?? null,
  setItem: (k: string, v: string) => void fakeSession.set(k, v),
});

let fetchResponseType: ResponseType = "basic";
let fetchStatus = 200;
let fetchCalls = 0;
let fetchURL = "";
let fetchRedirect: RequestRedirect | undefined;
const fakeFetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
  fetchCalls += 1;
  fetchURL = input.toString();
  fetchRedirect = init?.redirect;
  return { type: fetchResponseType, status: fetchStatus } as Response;
};

// Install globals before importing the module under test.
const g = globalThis as unknown as Record<string, unknown>;
g.EventSource = FakeEventSource;
g.document = fakeDocument;
g.window = fakeWindow;
g.fetch = fakeFetch;
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
  fetchResponseType = "basic";
  fetchStatus = 200;
  fetchCalls = 0;
  fetchURL = "";
  fetchRedirect = undefined;
  reloadCalls = 0;
  fakeSession.clear();
  delete fakeWindow.__SHELLEY_INIT__;
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
  let modelsChanged = 0;
  const statuses: string[] = [];
  let staleBuilds = 0;
  const handle = connectGlobalStream({
    getHash: () => "hash0",
    onListPatch: () => {},
    onModelsChanged: () => {
      modelsChanged += 1;
    },
    onStatusChange: (s) => statuses.push(s),
    onReconnect: () => {
      reconnects += 1;
    },
    onStaleBuild: () => {
      staleBuilds += 1;
    },
  });
  return {
    handle,
    statuses,
    get modelsChanged() {
      return modelsChanged;
    },
    get reconnects() {
      return reconnects;
    },
    get staleBuilds() {
      return staleBuilds;
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

await run("routes global model changes", () => {
  reset();
  const s = newStream();
  latest().emitMessage({ models_changed: true });
  assert(s.modelsChanged === 1, "global model change routed to App callback");
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
  now += 2000;
  fakeDocument.visibilityState = "visible";
  fakeDocument.dispatch("visibilitychange");
  fakeWindow.dispatch("pageshow");
  fakeWindow.dispatch("online");
  assert(!connecting.closed, "CONNECTING socket left intact");
  assert(FakeEventSource.instances.length === 1, "no extra EventSource opened");
  s.handle.close();
});

await run("resume replaces a stalled handshake and ignores its late callbacks", () => {
  reset();
  const s = newStream();
  latest().emitOpen();
  latest().emitError();
  advance(1000);
  const stale = latest();
  fakeDocument.visibilityState = "hidden";
  now += 85000;
  fakeDocument.visibilityState = "visible";
  fakeDocument.dispatch("visibilitychange");
  assert(stale.closed, "stalled handshake cancelled");
  assert(FakeEventSource.instances.length === 3, "fresh attempt started immediately");
  stale.emitOpen();
  stale.emitMessage({ heartbeat: true });
  stale.emitError();
  assert(s.statuses.at(-1) === "reconnecting", "late callbacks cannot change stream status");
  assert(s.reconnects === 0, "no extra backfill before the new connection opens");
  latest().emitOpen();
  assert(s.statuses.at(-1) === "connected", "new attempt connected");
  assert(s.reconnects === 1, "one backfill after recovery");
  s.handle.close();
});

await run("hung handshakes time out with the existing retry backoff", () => {
  reset();
  const s = newStream();
  for (const [deadline, backoff] of [
    [10000, 1000],
    [20000, 2000],
    [30000, 5000],
  ]) {
    const pending = latest();
    advance(deadline - 1);
    assert(!pending.closed, "handshake not cancelled before its deadline");
    advance(1);
    assert(pending.closed, "hung handshake cancelled");
    const status = s.statuses.at(-1);
    pending.emitOpen();
    pending.emitMessage({ heartbeat: true });
    pending.emitError();
    assert(s.statuses.at(-1) === status, "timed-out callbacks are inert during backoff");
    const count = FakeEventSource.instances.length;
    advance(backoff - 1);
    assert(FakeEventSource.instances.length === count, "retry not due early");
    advance(1);
    assert(FakeEventSource.instances.length === count + 1, "retried on the existing ladder");
  }
  s.handle.close();
  assert(timers.length === 0, "close cancels the pending handshake deadline");
  advance(120000);
  assert(FakeEventSource.instances.length === 4, "no retries after close");
});

await run("an open or first message cancels the handshake deadline", () => {
  for (const event of ["open", "message"]) {
    reset();
    const s = newStream();
    const live = latest();
    if (event === "open") live.emitOpen();
    else live.emitMessage({ heartbeat: true });
    advance(10000);
    assert(!live.closed, "connected stream survives the old handshake deadline");
    assert(fetchCalls === 0, "no authentication probe for a successful handshake");
    s.handle.close();
  }
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

await run("reloads once when the server runs a different UI build", async () => {
  reset();
  fakeWindow.__SHELLEY_INIT__ = { ui_build: "old" };
  let s = newStream();
  latest().emitOpen();
  latest().emitMessage({ models_changed: true, ui_build: "old" });
  assert(reloadCalls === 0 && s.staleBuilds === 0, "same build: no reload");
  // A visible page offers to reload, and reloads once the tab is hidden.
  latest().emitMessage({ models_changed: true, ui_build: "new" });
  assert(reloadCalls === 0 && s.staleBuilds === 1, "visible: offered a reload");
  fakeDocument.visibilityState = "hidden";
  fakeDocument.dispatch("visibilitychange");
  assert(reloadCalls === 1, "reloaded once hidden");
  s.handle.close();

  // A hidden page reloads right away.
  reset();
  fakeWindow.__SHELLEY_INIT__ = { ui_build: "old" };
  fakeDocument.visibilityState = "hidden";
  s = newStream();
  latest().emitOpen();
  latest().emitMessage({ models_changed: true, ui_build: "new" });
  assert(reloadCalls === 1 && s.staleBuilds === 0, "hidden: reloaded");
  assert(latest().closed, "closed the stream before reloading");
  s.handle.close();
  fakeDocument.visibilityState = "visible";

  // A page that still has the old build after reloading (a stale cache)
  // doesn't reload again for the same server build.
  s = newStream();
  latest().emitOpen();
  latest().emitMessage({ models_changed: true, ui_build: "new" });
  assert(reloadCalls === 1 && s.staleBuilds === 0, "no reload loop");
  s.handle.close();

  // Pages without a build (dev servers) never reload.
  reset();
  s = newStream();
  latest().emitOpen();
  latest().emitMessage({ models_changed: true, ui_build: "new" });
  assert(reloadCalls === 0, "no page build: no reload");
  s.handle.close();
});

await run("reloads when exe.dev requires authentication", async () => {
  reset();
  fetchResponseType = "opaqueredirect";
  const s = newStream();
  latest().emitOpen();

  latest().emitError();
  await Promise.resolve();
  await Promise.resolve();

  assert(fetchCalls === 1, "probed the same-origin Shelley endpoint once");
  assert(fetchURL === "/api/upload/raw", "used the cheap capability endpoint");
  assert(fetchRedirect === "manual", "kept the login redirect observable");
  assert(reloadCalls === 1, "reloaded into the exe.dev authentication flow");
  assert(timers.length === 0, "cancelled the reconnect timer before reloading");
  s.handle.close();
});

await run("reloads on an explicit authentication-required response", async () => {
  reset();
  fetchStatus = 401;
  const s = newStream();
  latest().emitOpen();

  latest().emitError();
  await Promise.resolve();
  await Promise.resolve();

  assert(reloadCalls === 1, "reloaded after the 401 response");
  s.handle.close();
});

await run("slow authentication probes can finish during retry backoff", async () => {
  reset();
  g.fetch = () =>
    new Promise<Response>((resolve) =>
      fakeSetTimeout(() => resolve({ type: "opaqueredirect", status: 0 } as Response), 3000),
    );
  const s = newStream();
  try {
    latest().emitOpen();
    latest().emitError();
    for (let step = 0; step < 12 && reloadCalls === 0; step++) {
      advance(1000);
      if (!latest().closed && latest().readyState === FakeEventSource.CONNECTING) {
        latest().emitError();
      }
      await Promise.resolve();
      await Promise.resolve();
    }
    assert(reloadCalls === 1, "slow sign-in probe reloads rather than retrying forever");
  } finally {
    s.handle.close();
    g.fetch = fakeFetch;
  }
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

await run("resource_status frames: a snapshot per connection, then transitions", () => {
  reset();
  const snapshots: { kind: string; episode_id: number }[][] = [];
  const transitions: { kind: string; revision: number }[] = [];
  const handle = connectGlobalStream({
    getHash: () => "hash0",
    onListPatch: () => {},
    onResourceSnapshot: (statuses) => snapshots.push(statuses),
    onResourceStatus: (status) => transitions.push(status),
  });
  const disk = {
    kind: "disk",
    episode_id: 1,
    revision: 1,
    active: true,
    dismissed: false,
    available_bytes: 1_600_000_000,
  };
  const memory = { ...disk, kind: "memory", episode_id: 3 };
  latest().emitOpen();
  latest().emitMessage({
    conversation_list_patch: { old_hash: "hash0", new_hash: "h1", patch: [] },
  });
  latest().emitMessage({ resource_status: disk });
  latest().emitMessage({ resource_status: memory });
  assert(snapshots.length === 0, "list replay and statuses open the connection");
  latest().emitMessage({ heartbeat: true });
  assert(snapshots.length === 1, "the next frame delivers the snapshot");
  assert(
    snapshots[0].map((s) => `${s.kind}:${s.episode_id}`).join() === "disk:1,memory:3",
    "snapshot carries every status in order",
  );
  latest().emitMessage({ resource_status: { ...memory, revision: 2 } });
  latest().emitMessage({ heartbeat: true });
  assert(
    snapshots.length === 1 && transitions.length === 1 && transitions[0].revision === 2,
    "later statuses are transitions",
  );
  // A restarted server may resend older revisions or monitor fewer kinds:
  // each connection delivers its own snapshot, even an empty one.
  latest().emitError();
  advance(1000);
  latest().emitOpen();
  latest().emitMessage({ resource_status: disk });
  latest().emitMessage({ models_changed: true });
  assert(
    snapshots.length === 2 && snapshots[1].length === 1 && snapshots[1][0].kind === "disk",
    "reconnect delivers a fresh snapshot",
  );
  latest().emitError();
  advance(2000);
  latest().emitOpen();
  latest().emitMessage({ heartbeat: true });
  assert(snapshots.length === 3 && snapshots[2].length === 0, "no statuses is an empty snapshot");
  handle.close();
});

Date.now = realDateNow;
console.log("\nglobalStream: all scenarios passed");
