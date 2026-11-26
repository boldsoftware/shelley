// Cheap recomputation counters for the performance HUD.
//
// Call sites sprinkle perfCount("name") (or perfWrap for timed sections) into
// hot reactive paths: computeds that walk all messages, store notifications,
// scroll/resize handlers, markdown rendering, etc. The counters are plain
// (non-reactive) Map entries, so counting costs a Map lookup and two integer
// adds — safe to leave enabled unconditionally. The HUD (behind the
// `performance-hud` feature flag) polls perfSnapshot() on an interval; nothing
// reactive depends on the counters, so instrumentation itself never triggers
// re-renders.
//
// Units: totalMs accumulates float milliseconds from performance.now(), the
// finest timer the web platform exposes (there is no nanosecond clock in
// browsers). Its µs-scale fractions are deliberately coarsened as a Spectre
// mitigation (Chrome ~100µs, Firefox ~1ms), so one cheap call may measure 0,
// but totals over many calls are statistically sound. Only explicitly
// bracketed spans (perfWrap, longtasks) are timed; bare perfCount sites —
// including component mount/update hooks, which Vue runs post-flush where
// spans would overlap and over-count — report totalMs 0 by design.
//
// Console access for humans and agents (always installed):
//   __shelleyPerf.snapshot()  -> { name: { count, totalMs } }
//   __shelleyPerf.log()       -> console.table of the same
//   __shelleyPerf.reset()
//   __shelleyPerf.delta(ms)   -> Promise of counts accrued over the window
//   __shelleyPerf.longTasks() -> recent >50ms main-thread blocks + suspects
//   __shelleyPerf.loads()     -> recent conversation load source + phase timings

export interface PerfCounter {
  count: number;
  totalMs: number;
}

export type ConversationLoadSource = "memory" | "indexeddb" | "incremental" | "network";

/** One completed conversation load, including the browser work that happens
 * after data is available. `renderMs` spans Vue patching plus the first paint,
 * so it remains useful in Safari where the Long Tasks API is unavailable. */
export interface ConversationLoad {
  conversationId: string;
  source: ConversationLoadSource;
  messages: number;
  bytes: number;
  hydrateMs: number;
  fetchMs: number;
  renderMs: number;
  totalMs: number;
  completedAt: number;
}

const counters = new Map<string, PerfCounter>();
const conversationLoads: ConversationLoad[] = [];
const CONVERSATION_LOAD_BUFFER = 20;

/** Increment a named counter, optionally accumulating elapsed milliseconds. */
export function perfCount(name: string, durMs?: number): void {
  let c = counters.get(name);
  if (!c) {
    c = { count: 0, totalMs: 0 };
    counters.set(name, c);
  }
  c.count++;
  if (durMs !== undefined) c.totalMs += durMs;
}

/** Wrap a getter (e.g. a computed body) so each invocation is counted and
 *  timed. */
export function perfWrap<T>(name: string, fn: () => T): () => T {
  return () => {
    const start = performance.now();
    try {
      return fn();
    } finally {
      perfCount(name, performance.now() - start);
    }
  };
}

/** Copy of all counters. */
export function perfSnapshot(): Record<string, PerfCounter> {
  const out: Record<string, PerfCounter> = {};
  for (const [name, c] of counters) {
    out[name] = { count: c.count, totalMs: c.totalMs };
  }
  return out;
}

export function perfRecordConversationLoad(load: Omit<ConversationLoad, "completedAt">): void {
  const recorded = { ...load, completedAt: Date.now() };
  conversationLoads.push(recorded);
  if (conversationLoads.length > CONVERSATION_LOAD_BUFFER) conversationLoads.shift();
  perfCount(`conversationLoad.${load.source}`, load.totalMs);
}

/** Completed loads, oldest first. */
export function perfConversationLoads(): ConversationLoad[] {
  return conversationLoads.map((load) => ({ ...load }));
}

export function perfReset(): void {
  counters.clear();
  conversationLoads.length = 0;
  longTasks.length = 0;
  lastLongTaskCounts.clear();
}

// ── Long tasks ─────────────────────────────────────────────────────────────────────
//
// Main-thread blocks >50ms, observed for the whole page lifetime (not just
// while the HUD is open) with `buffered: true` so tasks from initial page
// load are captured too. The Longtask API's own attribution is nearly
// useless (usually just "self"), so instead we record which counters
// incremented since the PREVIOUS long task as cheap suspects — during a
// stall the intervening increments almost always belong to it.

export interface LongTask {
  /** Task start, on the performance.now() timeline. */
  startMs: number;
  durMs: number;
  /** Counters that incremented since the previous long task, busiest first,
   *  formatted "name×delta". Heuristic, not proof. */
  suspects: string[];
}

const LONGTASK_BUFFER = 20;
const longTasks: LongTask[] = [];
const lastLongTaskCounts = new Map<string, number>();

function noteLongTask(entry: PerformanceEntry): void {
  perfCount("browser.longtask", entry.duration);
  const suspects: { name: string; delta: number }[] = [];
  for (const [name, c] of counters) {
    if (name === "browser.longtask") continue;
    const delta = c.count - (lastLongTaskCounts.get(name) ?? 0);
    if (delta > 0) suspects.push({ name, delta });
  }
  suspects.sort((a, b) => b.delta - a.delta);
  lastLongTaskCounts.clear();
  for (const [name, c] of counters) lastLongTaskCounts.set(name, c.count);
  longTasks.push({
    startMs: entry.startTime,
    durMs: entry.duration,
    suspects: suspects.slice(0, 4).map((s) => (s.delta > 1 ? `${s.name}×${s.delta}` : s.name)),
  });
  if (longTasks.length > LONGTASK_BUFFER) longTasks.shift();
}

/** Recent long tasks, oldest first (last LONGTASK_BUFFER). */
export function perfLongTasks(): LongTask[] {
  return longTasks.slice();
}

/** Counts accrued over a sampling window; handy from the console:
 *  await __shelleyPerf.delta(2000) while typing/resizing. */
async function perfDelta(windowMs = 1000): Promise<Record<string, PerfCounter>> {
  const before = perfSnapshot();
  await new Promise((r) => setTimeout(r, windowMs));
  const after = perfSnapshot();
  const out: Record<string, PerfCounter> = {};
  for (const [name, c] of Object.entries(after)) {
    const b = before[name];
    const count = c.count - (b?.count ?? 0);
    const totalMs = c.totalMs - (b?.totalMs ?? 0);
    if (count !== 0) out[name] = { count, totalMs };
  }
  return out;
}

function perfLog(): void {
  const rows = Object.entries(perfSnapshot())
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([name, c]) => ({
      name,
      count: c.count,
      totalMs: Math.round(c.totalMs * 10) / 10,
    }));
  console.table(rows);
}

declare global {
  interface Window {
    __shelleyPerf?: {
      snapshot: typeof perfSnapshot;
      reset: typeof perfReset;
      log: typeof perfLog;
      delta: typeof perfDelta;
      count: typeof perfCount;
      longTasks: typeof perfLongTasks;
      loads: typeof perfConversationLoads;
    };
  }
}

if (typeof window !== "undefined") {
  window.__shelleyPerf = {
    snapshot: perfSnapshot,
    reset: perfReset,
    log: perfLog,
    delta: perfDelta,
    count: perfCount,
    longTasks: perfLongTasks,
    loads: perfConversationLoads,
  };
  // Baseline browser events worth correlating against: window resizes.
  window.addEventListener("resize", () => perfCount("browser.windowResize"));
  if (
    typeof PerformanceObserver !== "undefined" &&
    PerformanceObserver.supportedEntryTypes?.includes("longtask")
  ) {
    try {
      new PerformanceObserver((list) => {
        for (const entry of list.getEntries()) noteLongTask(entry);
      }).observe({ type: "longtask", buffered: true });
    } catch {
      // Registration failed; leave long-task tracking disabled.
    }
  }
}
