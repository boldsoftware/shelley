// Fixes for ghostty-web@0.4.0-next.14 behaviour, installed on each terminal
// after open(). test/e2e_terminal_ghostty_test.go covers them.

import {
  cursorKeyBytes,
  mouseWheelBytes,
  terminalScrollGesture,
  WheelRowAccumulator,
  type MouseReportFormat,
  type TerminalScrollModes,
} from "./terminalScrollPolicy";

// DEC private modes the scroll decision reads. Numbers, not names, because
// that's what ghostty-web's getMode() takes.
const DEC_CURSOR_KEYS_APPLICATION = 1; // DECCKM
const DEC_ALTERNATE_SCROLL = 1007;
const DEC_MOUSE_SGR = 1006;
const DEC_MOUSE_URXVT = 1015;
const DEC_MOUSE_SGR_PIXELS = 1016;

// The ghostty-web Terminal surface the wheel policy needs. All optional: a
// backend that predates one of these (or a test double) degrades to "leave the
// wheel alone" rather than throwing inside an event handler.
export interface GhosttyWheelTerminal {
  readonly cols: number;
  readonly rows: number;
  element?: HTMLElement;
  attachCustomWheelEventHandler?(cb?: (ev: WheelEvent) => boolean): void;
  input?(data: string, wasUserInput?: boolean): void;
  getMode?(mode: number, isAnsi?: boolean): boolean;
  hasMouseTracking?(): boolean;
  buffer?: { active?: { type?: string } };
  wasmTerm?: { isAlternateScreen?(): boolean };
}

// Every read here goes through the emulator, which throws from assertOpen()
// before open() and after dispose() — and this runs inside a DOM event
// handler, where a throw would leave the page with a half-handled wheel tick.
function safely<T>(read: () => T, fallback: T): T {
  try {
    const v = read();
    return v === undefined ? fallback : v;
  } catch {
    return fallback;
  }
}

function readGhosttyScrollModes(term: GhosttyWheelTerminal): TerminalScrollModes {
  const mode = (n: number, fallback: boolean) =>
    term.getMode ? safely(() => term.getMode!(n, false), fallback) : fallback;
  return {
    // buffer.active.type is the public spelling; wasmTerm.isAlternateScreen()
    // is the same bit one layer down, kept as a fallback because the buffer
    // namespace is lazily built.
    onAlternateScreen:
      safely(() => term.buffer?.active?.type, undefined) === "alternate" ||
      safely(() => term.wasmTerm?.isAlternateScreen?.(), false) === true,
    mouseTrackingActive: term.hasMouseTracking
      ? safely(() => term.hasMouseTracking!(), false)
      : false,
    // 1007 defaults to ON in ghostty (terminal/modes.zig), and the fallback
    // matters: defaulting it off would swallow the wheel in `less` on any
    // backend that can't answer.
    alternateScrollEnabled: mode(DEC_ALTERNATE_SCROLL, true),
    cursorKeysApplicationMode: mode(DEC_CURSOR_KEYS_APPLICATION, false),
  };
}

function mouseReportFormat(term: GhosttyWheelTerminal): MouseReportFormat {
  const mode = (n: number) => (term.getMode ? safely(() => term.getMode!(n, false), false) : false);
  // Order matters: an app that sets both 1006 and 1016 wants pixels.
  if (mode(DEC_MOUSE_SGR_PIXELS)) return "sgrPixels";
  if (mode(DEC_MOUSE_SGR)) return "sgr";
  if (mode(DEC_MOUSE_URXVT)) return "urxvt";
  return "x10";
}

// Where in the grid the pointer is, plus the cell height the row quantization
// needs. ghostty sizes its canvas to exactly cols×rows cells, so the canvas
// rect divides evenly; the host element can be larger (letterboxing) and would
// skew the mapping, hence the querySelector.
function wheelGeometry(term: GhosttyWheelTerminal, ev: WheelEvent) {
  const host = term.element;
  const canvas = host?.querySelector("canvas") ?? host;
  const rect = canvas?.getBoundingClientRect();
  const cols = Math.max(1, term.cols || 1);
  const rows = Math.max(1, term.rows || 1);
  if (!rect || rect.width <= 0 || rect.height <= 0) {
    return { col: 1, row: 1, x: 1, y: 1, cellHeight: 0, rows };
  }
  const cellWidth = rect.width / cols;
  const cellHeight = rect.height / rows;
  // Synthesized wheel events (touch drags, see attachTouch) carry no
  // pointer position; report the top-left cell rather than NaN coordinates,
  // which would decode as a random cell — apps that scroll from the wheel
  // ignore the position anyway.
  const at = (v: number) => (Number.isFinite(v) ? v : 0);
  const x = at(ev.clientX) - rect.left;
  const y = at(ev.clientY) - rect.top;
  const clamp = (v: number, hi: number) => Math.min(Math.max(v, 1), hi);
  return {
    col: clamp(Math.floor(x / cellWidth) + 1, cols),
    row: clamp(Math.floor(y / cellHeight) + 1, rows),
    x: Math.max(1, Math.round(x)),
    y: Math.max(1, Math.round(y)),
    cellHeight,
    rows,
  };
}

/**
 * Give the wheel to whoever asked for it.
 *
 * ghostty-web's own handleWheel() turns every wheel tick on the alternate
 * screen into ESC[A/ESC[B without asking whether the application enabled mouse
 * reporting, so a mouse-reporting TUI (Claude Code, tmux `mouse on`, htop) gets
 * arrow keys it never wanted — they walk the prompt instead of scrolling the
 * view. handleWheel() consults customWheelEventHandler first and returns early
 * when it's truthy, which is the seam this uses: decide with
 * terminalScrollGesture(), send the bytes ourselves, and only fall through to
 * ghostty for the case it gets right (its own scrollback).
 */
export function installGhosttyWheelPolicy(term: GhosttyWheelTerminal) {
  if (!term.attachCustomWheelEventHandler || !term.input) {
    throw new Error("ghostty-web has no custom wheel handler or input()");
  }

  const accumulator = new WheelRowAccumulator();
  term.attachCustomWheelEventHandler((ev: WheelEvent): boolean => {
    const modes = readGhosttyScrollModes(term);
    // The .viewport branch of the policy: primary screen, no mouse reporting.
    // Hand it back to ghostty, whose viewport scrolls by fractional rows and
    // knows about its own scrollbar. Checked before quantizing so the
    // accumulator never banks rows it didn't emit.
    if (!modes.mouseTrackingActive && !modes.onAlternateScreen) {
      accumulator.reset();
      return false;
    }
    const geo = wheelGeometry(term, ev);
    const rows = accumulator.consume(ev, geo.cellHeight, geo.rows);
    const gesture = terminalScrollGesture(rows, modes);
    switch (gesture.kind) {
      case "mouseWheel":
        term.input!(
          mouseWheelBytes({
            count: gesture.count,
            up: gesture.up,
            format: mouseReportFormat(term),
            col: geo.col,
            row: geo.row,
            x: geo.x,
            y: geo.y,
          }),
          true,
        );
        return true;
      case "cursorKeys":
        term.input!(cursorKeyBytes(gesture.count, gesture.up, gesture.applicationMode), true);
        return true;
      case "viewport":
        // Unreachable: the early return above covers every viewport case.
        // Falling through to ghostty is the safe reading of it regardless.
        accumulator.reset();
        return false;
      case "none":
        // Either sub-row travel we've banked, or an app that wants neither
        // reports nor arrows. Either way this tick is ours, not ghostty's.
        return true;
    }
  });
}

// The pinned ghostty-web parses DEC2026, but CanvasRenderer ignores it and
// clears the WASM dirty state on every render. Gate that single paint entry
// point, NOT writes: parsing, generated replies, and input must keep flowing.
// This uses parsed mode state (including split byte sequences), not a second
// escape-sequence parser. DECRQM replies remain the upstream engine's concern.
// renderer.render is the exported CanvasRenderer's common paint entry point;
// the pinned package also exposes renderer to its own FitAddon.
const GHOSTTY_SYNC_TIMEOUT_MS = 1000;

export interface GhosttySynchronizedTerminal {
  getMode?(mode: number, isAnsi?: boolean): boolean;
  write(data: string | Uint8Array, callback?: () => void): void;
  resize?(cols: number, rows: number): void;
  readonly cols?: number;
  readonly rows?: number;
  renderer?: {
    render(buffer: unknown, force?: boolean, ...rest: unknown[]): void;
  };
}

/** Returns a function that removes the hold and its deadline timer. */
export function installGhosttySynchronizedOutput(term: GhosttySynchronizedTerminal): () => void {
  const renderer = term.renderer;
  if (!renderer || !term.getMode) {
    throw new Error("ghostty-web has no renderer or getMode()");
  }
  const render = renderer.render;
  const write = term.write;
  const resize = term.resize;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let active = false;
  let expired = false;
  let force = false;
  let disposed = false;
  let lastArgs: Parameters<typeof render> | undefined;

  const cancelTimer = () => {
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
  };
  // Fail open for the rest of this held mode after the deadline. Do not
  // repeatedly hide output for another second, or change the emulator's mode.
  const release = () => {
    cancelTimer();
    expired = true;
    force = true;
  };
  const shouldHold = () => {
    const enabled = safely(() => term.getMode!(2026, false), false);
    if (enabled && !active) {
      active = true;
      expired = false;
      force = true;
      timer = setTimeout(() => {
        if (disposed) return;
        release();
        if (lastArgs) renderer.render(...lastArgs);
      }, GHOSTTY_SYNC_TIMEOUT_MS);
    } else if (!enabled && active) {
      cancelTimer();
      active = false;
      expired = false;
      force = true;
    }
    return active && !expired;
  };

  renderer.render = function (...args) {
    if (disposed) return;
    lastArgs = args;
    if (shouldHold()) return; // Leave WASM dirty state untouched.
    render.call(this, args[0], args[1] || force, ...args.slice(2));
    force = false;
  };
  term.write = function (data, callback) {
    write.call(this, data, callback);
    // Observe resets even if a new update begins before the next RAF. This
    // also starts the deadline at parsing time, not the next visible frame.
    shouldHold();
  };
  if (resize) {
    term.resize = function (cols, rows) {
      // Resizing clears the canvas outside render(). End the visual hold so
      // it repaints immediately rather than leaving a blank resized canvas.
      if (active && (cols !== term.cols || rows !== term.rows)) release();
      resize.call(this, cols, rows);
    };
  }
  return () => {
    disposed = true;
    cancelTimer();
    lastArgs = undefined;
    renderer.render = render;
    term.write = write;
    if (resize) term.resize = resize;
  };
}

/**
 * In the pinned package both Terminal.focus() and its SelectionManager focus
 * the contenteditable host, but beforeinput is handled ONLY on its hidden
 * textarea. ASCII keydown works on either; Unicode/IME insertText (and so
 * most soft keyboards) is lost on the host because its beforeinput listener
 * just preventDefaults it. Returns a function that removes the redirect.
 */
export function installGhosttyInputFocus(host: HTMLElement): () => void {
  const input = host.querySelector("textarea");
  if (!input) throw new Error("ghostty-web has no input textarea");
  const redirect = () => {
    if (host.ownerDocument.activeElement === host) input.focus({ preventScroll: true });
  };
  // focus does not bubble: focusing the textarea cannot recurse here. Only
  // redirect an existing focus intent, never wheel/touchmove/scroll events.
  host.addEventListener("focus", redirect);
  redirect(); // open() may have already focused the host.
  return () => host.removeEventListener("focus", redirect);
}

// The seam the selection policy uses: ghostty-web's InputHandler gates every
// mouse report on mouseConfig.hasMouseTracking(). None of this is public API.
export interface GhosttyMouseTerminal {
  element?: HTMLElement;
  inputHandler?: {
    mouseConfig?: { hasMouseTracking?: () => boolean };
    sendMouseEvent?: (
      button: number,
      col: number,
      row: number,
      release: boolean,
      ev?: MouseEvent,
    ) => void;
  };
  selectionManager?: { clearSelection?: () => void; isSelecting?: boolean };
}

/**
 * Selection versus mouse tracking, the way desktop terminals do it: while an
 * application has mouse reporting on, a plain gesture belongs to the
 * application and Shift+drag (or Shift+double-click) is the terminal's own
 * selection.
 *
 * ghostty-web@0.4.0-next.14 does neither. Its SelectionManager starts a
 * selection on every drag, but the InputHandler reports the same
 * mousedown/mousemove/mouseup to the application, and each report goes
 * through Terminal.onDataCallback, which clears the selection -- so a drag in
 * a mouse-tracking app selects nothing, Shift or no Shift. Its double- and
 * triple-click handlers, on the other hand, always select and copy a word or
 * line, even though the application received the same clicks and may be
 * selecting and copying (via OSC 52) on its own; the two writes then race for
 * the clipboard with different text.
 *
 * So, while the application tracks the mouse:
 *  - With Shift held on the primary press, the InputHandler's tracking gate
 *    answers false until the release, so no report is generated and the
 *    selection survives to mouseup, where ghostty's copy-on-select handles
 *    it. The flag is set from a capture listener on the host and cleared from
 *    a bubble listener on the document: the InputHandler's listeners sit
 *    between those two points in the event path, so the press, the drag and
 *    the release itself are all hidden from it (a lone release report would
 *    clear the selection just the same). A release the document never sees --
 *    outside the window, or swallowed upstream -- is caught up with on the
 *    next motion with the button up.
 *  - Without Shift, multi-click events are stopped at the host before they
 *    reach the SelectionManager's canvas listener, so ghostty neither selects
 *    nor copies; the application does, if it wants to.
 *  - Mouse reports go through the same data callback as typing, which clears
 *    the selection. Only a primary press keeps that effect: an application
 *    with any-event tracking (Claude Code) hears about every motion, and a
 *    selection that vanished on the first mouse movement could never reach
 *    the right-click that copies it.
 * Terminal.hasMouseTracking() is left truthful; only the InputHandler's view
 * changes.
 *
 * Returns a function that removes the policy.
 */
export function installGhosttySelectionPolicy(term: GhosttyMouseTerminal): () => void {
  const host = term.element;
  const gate = term.inputHandler?.mouseConfig;
  if (!host || !gate || typeof gate.hasMouseTracking !== "function") {
    throw new Error("ghostty-web has no mouse tracking gate");
  }

  const original = gate.hasMouseTracking;
  const tracking = () => original.call(gate);
  let nativeGesture = false;

  const handler = term.inputHandler!;
  const selection = term.selectionManager;
  const send = handler.sendMouseEvent;
  const clear = selection?.clearSelection;
  let keepSelection = false;
  if (send && clear) {
    handler.sendMouseEvent = function (button, col, row, release, ev) {
      keepSelection = button !== 0 || release;
      try {
        send.call(this, button, col, row, release, ev);
      } finally {
        keepSelection = false;
      }
    };
    selection.clearSelection = function () {
      if (keepSelection) return;
      clear.call(this);
      // A press report arrives while the SelectionManager's own selection is
      // still collapsed on the pressed cell, which clearSelection() ignores;
      // the motion reports used to end that gesture, so end it here instead.
      this.isSelecting = false;
    };
  }
  // Whether the press behind the current click sequence carried Shift; the
  // click event arrives after mouseup has already cleared nativeGesture.
  let pressWasLocal = false;
  gate.hasMouseTracking = () => !nativeGesture && tracking();

  const onMouseDown = (ev: MouseEvent) => {
    if (ev.button !== 0) return;
    pressWasLocal = ev.shiftKey;
    nativeGesture = ev.shiftKey && tracking();
  };
  const onMouseUp = (ev: MouseEvent) => {
    if (ev.button === 0) nativeGesture = false;
  };
  const onMouseMove = (ev: MouseEvent) => {
    if (nativeGesture && !(ev.buttons & 1)) nativeGesture = false;
  };
  const onBlur = () => {
    nativeGesture = false;
  };
  const onClick = (ev: MouseEvent) => {
    if (ev.detail >= 2 && !pressWasLocal && tracking()) ev.stopPropagation();
  };
  host.addEventListener("mousedown", onMouseDown, { capture: true });
  host.addEventListener("click", onClick, { capture: true });
  document.addEventListener("mouseup", onMouseUp);
  document.addEventListener("mousemove", onMouseMove);
  window.addEventListener("blur", onBlur);
  return () => {
    host.removeEventListener("mousedown", onMouseDown, { capture: true });
    host.removeEventListener("click", onClick, { capture: true });
    document.removeEventListener("mouseup", onMouseUp);
    document.removeEventListener("mousemove", onMouseMove);
    window.removeEventListener("blur", onBlur);
    nativeGesture = false;
    gate.hasMouseTracking = original;
    if (send && clear) {
      handler.sendMouseEvent = send;
      selection.clearSelection = clear;
    }
  };
}

export interface GhosttyKeyTerminal {
  inputHandler?: {
    container?: HTMLElement;
    keydownListener?: ((ev: KeyboardEvent) => void) | null;
  };
}

/**
 * ghostty-web encodes every Cmd (Super) chord but Cmd+C and Cmd+V for the
 * program and cancels it, even when the encoding is empty, so browser and app
 * shortcuts (Cmd+R, Cmd+L, Cmd+P) die in a focused terminal. xterm.js leaves
 * them to the browser, and so does this: they skip ghostty's keydown listener.
 * Its dispose() removes whichever listener is in keydownListener.
 */
export function installGhosttyMetaPassthrough(term: GhosttyKeyTerminal) {
  const handler = term.inputHandler;
  const container = handler?.container;
  const listener = handler?.keydownListener;
  if (!handler || !container || !listener) {
    throw new Error("ghostty-web has no keydown listener");
  }
  const passthrough = (ev: KeyboardEvent) => {
    if (ev.metaKey && ev.code !== "KeyC" && ev.code !== "KeyV") return;
    listener(ev);
  };
  container.removeEventListener("keydown", listener);
  container.addEventListener("keydown", passthrough);
  handler.keydownListener = passthrough;
}
