// A fake clock, as Playwright's page.clock has it. The harness installs it
// before the page's own scripts with window.__CLOCK_START__ set to the epoch
// milliseconds it starts at (or undefined for now). Time then flows as real
// time does until a test pauses it, and steps drive it with window.__clock:
//
//   pauseAt(ms)      jump forward to ms (epoch milliseconds or a date string),
//                    firing each late timer once, then stop time there
//   runFor(ms)       advance ms, firing every timer due in that span in order,
//                    intervals as often as they come due
//   fastForward(ms)  jump ms ahead, firing each late timer once
//   setFixedTime(ms) make Date report ms from now on; timers run as before
//   resume()         let time flow again
//
// pauseAt, runFor and fastForward return promises (an eval step awaits
// them): like Playwright's, they let the page run a task after each timer,
// so promise chains and awaited sleeps advance as they would in real time.
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
  let firing = false; // in a timer's callback
  let advancing = 0; // advances running, which own the time
  let began = 0; // real time the outermost of them began
  const timers = new Map();

  const current = () => (paused || advancing ? now : now + (real.now() - realAt));
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
    delay = Math.max(0, Number(delay) || 0);
    // As Playwright's, a timer set from a timer's callback is never due in
    // the same instant.
    if (firing) delay = Math.max(1, delay);
    timers.set(id, { id, kind, fn, args, interval, at: current() + delay });
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
    firing = true;
    try {
      if (t.kind === 'frame') t.fn(performance.now());
      else if (t.kind === 'idle') t.fn({ didTimeout: false, timeRemaining: () => 50 });
      else if (typeof t.fn === 'function') t.fn(...t.args);
      else (0, eval)(String(t.fn));
    } catch (e) {
      reportError(e);
    } finally {
      firing = false;
    }
  };
  const due = (until) => {
    let next = null;
    for (const t of timers.values()) {
      if (t.at <= until && (!next || t.at < next.at || (t.at === next.at && t.id < next.id))) next = t;
    }
    return next;
  };
  const nextTask = () => new Promise((done) => real.setTimeout(done));
  // advance moves time to target(now), firing what comes due in order and
  // letting the page run a task after each. With once, timers already late
  // by then fire once, all at the target time, as after a laptop's lid opens.
  const advance = async (target, once) => {
    rebase();
    const until = target(now);
    if (until < now) throw new Error('the clock cannot go back from ' + new RealDate(now).toISOString());
    if (!advancing++) began = real.now();
    schedule(); // no wake-ups meanwhile
    try {
      if (once) {
        for (const t of timers.values()) if (t.at <= until) t.at = until;
        now = until;
      }
      for (let t = due(until); t; t = due(until)) {
        now = Math.max(now, t.at);
        fire(t);
        await nextTask();
      }
    } finally {
      advancing--;
      now = Math.max(now, until); // another advance may have gone further
      // Flowing time went on while the timers fired; without this the page's
      // clock falls behind the server's by a task per timer.
      if (!advancing && !paused) now += real.now() - began;
      realAt = real.now();
      schedule();
    }
  };
  // schedule wakes up for the next timer while time flows.
  function schedule() {
    if (wake !== null) real.clearTimeout(wake);
    wake = null;
    if (paused || advancing || timers.size === 0) return;
    let at = Infinity;
    for (const t of timers.values()) at = Math.min(at, t.at);
    wake = real.setTimeout(() => {
      wake = null;
      advance((now) => now);
    }, Math.max(0, at - current()));
  }

  window.__clock = {
    pauseAt(t) {
      rebase();
      paused = true;
      return advance(() => toMs(t), true);
    },
    runFor(ms) {
      return advance((now) => now + ms);
    },
    fastForward(ms) {
      return advance((now) => now + ms, true);
    },
    setFixedTime(t) {
      fixed = toMs(t);
    },
    resume() {
      rebase();
      paused = false;
      began = real.now();
      schedule();
    },
  };
})();
