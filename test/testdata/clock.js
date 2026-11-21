// A fake clock, as Playwright's page.clock has it. The harness installs it
// before the page's own scripts with window.__CLOCK_START__ set to the epoch
// milliseconds it starts at (or undefined for now). Time then flows as real
// time does until a test pauses it, and steps drive it with window.__clock:
//
//   pauseAt(ms)      jump forward to ms (epoch milliseconds or a date string),
//                    firing each due timer once, and stop time there
//   runFor(ms)       advance ms, firing every timer due in that span in order,
//                    intervals as often as they come due
//   fastForward(ms)  jump ms ahead, firing each due timer once
//   setFixedTime(ms) make Date report ms from now on; timers run as before
//   resume()         let time flow again
//
// Date, performance.now, setTimeout, setInterval, requestAnimationFrame and
// requestIdleCallback (and their cancel functions) follow the fake clock.
(() => {
  const RealDate = Date;
  const real = {
    setTimeout: setTimeout.bind(window),
    clearTimeout: clearTimeout.bind(window),
    now: performance.now.bind(performance),
  };
  const start = window.__CLOCK_START__ ?? RealDate.now();
  const perfStart = real.now();
  let now = start; // fake epoch milliseconds when time was last rebased
  let realAt = real.now(); // real time of that rebase
  let paused = false;
  let fixed = null;
  let nextId = 1;
  let wake = null;
  const timers = new Map();

  const current = () => (paused ? now : now + (real.now() - realAt));
  const rebase = () => {
    now = current();
    realAt = real.now();
  };
  const toMs = (t) => (typeof t === 'number' ? t : new RealDate(t).getTime());

  function FakeDate(...args) {
    if (!new.target) return new RealDate(FakeDate.now()).toString();
    return args.length ? new RealDate(...args) : new RealDate(FakeDate.now());
  }
  FakeDate.prototype = RealDate.prototype;
  FakeDate.now = () => fixed ?? Math.floor(current());
  FakeDate.parse = RealDate.parse;
  FakeDate.UTC = RealDate.UTC;
  window.Date = FakeDate;
  performance.now = () => perfStart + (current() - start);

  const add = (kind, fn, delay, args, interval) => {
    const id = nextId++;
    timers.set(id, { id, kind, fn, args, interval, at: current() + Math.max(0, Number(delay) || 0) });
    schedule();
    return id;
  };
  const remove = (id) => {
    timers.delete(id);
    schedule();
  };
  window.setTimeout = (fn, delay, ...args) => add('timeout', fn, delay, args);
  window.setInterval = (fn, delay, ...args) => add('interval', fn, delay, args, Math.max(1, Number(delay) || 0));
  window.clearTimeout = window.clearInterval = remove;
  window.requestAnimationFrame = (fn) => add('frame', fn, 16 - ((current() - start) % 16));
  window.cancelAnimationFrame = remove;
  window.requestIdleCallback = (fn) => add('idle', fn, 0);
  window.cancelIdleCallback = remove;

  const fire = (t) => {
    if (t.interval) t.at += t.interval;
    else timers.delete(t.id);
    try {
      if (t.kind === 'frame') t.fn(performance.now());
      else if (t.kind === 'idle') t.fn({ didTimeout: false, timeRemaining: () => 50 });
      else if (typeof t.fn === 'function') t.fn(...t.args);
      else (0, eval)(String(t.fn));
    } catch (e) {
      reportError(e);
    }
  };
  const due = (until) => {
    let next = null;
    for (const t of timers.values()) {
      if (t.at <= until && (!next || t.at < next.at || (t.at === next.at && t.id < next.id))) next = t;
    }
    return next;
  };
  // advance moves time to until, firing what comes due. With once, each
  // timer due fires at most once, as after a laptop's lid opens.
  const advance = (until, once) => {
    rebase();
    if (until < now) throw new Error('the clock cannot go back from ' + new RealDate(now).toISOString());
    if (once) {
      for (const t of [...timers.values()].filter((t) => t.at <= until).sort((a, b) => a.at - b.at || a.id - b.id)) {
        if (!timers.has(t.id)) continue;
        now = Math.max(now, t.at);
        fire(t);
        if (t.interval && t.at <= until) t.at = until + t.interval;
      }
    } else {
      for (let t = due(until); t; t = due(until)) {
        now = Math.max(now, t.at);
        fire(t);
      }
    }
    now = until;
    realAt = real.now();
    schedule();
  };
  // schedule wakes up for the next timer while time flows.
  function schedule() {
    if (wake !== null) real.clearTimeout(wake);
    wake = null;
    if (paused || timers.size === 0) return;
    let at = Infinity;
    for (const t of timers.values()) at = Math.min(at, t.at);
    wake = real.setTimeout(() => {
      wake = null;
      advance(current());
    }, Math.max(0, at - current()));
  }

  window.__clock = {
    pauseAt(t) {
      advance(toMs(t), true);
      paused = true;
      schedule();
    },
    runFor(ms) {
      advance(current() + ms);
    },
    fastForward(ms) {
      advance(current() + ms, true);
    },
    setFixedTime(t) {
      fixed = toMs(t);
    },
    resume() {
      rebase();
      paused = false;
      schedule();
    },
  };
})();
