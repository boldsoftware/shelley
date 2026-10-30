// The two terminal emulators the terminal panel can render with, behind one
// small interface. ghostty-web (Ghostty's emulator compiled to WebAssembly)
// is the default; the panel's menu switches to xterm.js.
//
// ghostty-web, WebAssembly included, is built into its own bundle
// (scripts/build.js) and fetched when the first terminal opens.

import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon as XTermFitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { WebglAddon } from "@xterm/addon-webgl";
import type * as GhosttyWeb from "ghostty-web";
import {
  installGhosttyInputFocus,
  installGhosttyMetaPassthrough,
  installGhosttySelectionPolicy,
  installGhosttySynchronizedOutput,
  installGhosttyWheelPolicy,
  type GhosttyKeyTerminal,
  type GhosttyMouseTerminal,
  type GhosttySynchronizedTerminal,
  type GhosttyWheelTerminal,
} from "./ghosttyPolicies";
import { installGhosttyWrappedUrls, type GhosttyLinkTerminal } from "./ghosttyLinks";
import { getTerminalTheme } from "./terminalHelpers";
import { attachTerminalTouch } from "./terminalTouch";

export type TerminalRenderer = "ghostty" | "xterm";

export interface TerminalView {
  readonly cols: number;
  readonly rows: number;
  write(data: Uint8Array | string): void;
  focus(): void;
  paste(data: string): void;
  input(data: string): void;
  setFontSize(size: number): void;
  /** Size the grid to the container. */
  fit(): void;
  /** The visible screen, or the whole buffer, one string per row. */
  lines(which: "screen" | "all"): string[];
  /** Switch color scheme. False if the renderer can't in place. */
  setTheme(isDark: boolean): boolean;
  dispose(): void;
}

/** What the terminal panel uses of a terminal. */
export type TerminalHandle = Pick<TerminalView, "focus" | "lines" | "paste" | "input">;

export interface TerminalViewOptions {
  isDark: boolean;
  fontSize: number;
  onFontSizeChange(size: number): void;
  /** Size until the first fit, e.g. while hidden. */
  cols?: number;
  rows?: number;
  /** Keyboard and paste input for the program. */
  onData(data: string): void;
  /** The cell size changed, so the grid needs fitting again. */
  onCellSizeChange(): void;
}

const FONT_FAMILY = 'Consolas, "Liberation Mono", Menlo, Courier, monospace';
const ICON_FONT = '"Symbols Nerd Font Mono"'; // see src/styles.css

/**
 * Open a terminal in a new element appended to `container`. dispose()
 * removes it again, so a container can host one renderer after another.
 */
export async function openTerminalView(
  renderer: TerminalRenderer,
  container: HTMLElement,
  opts: TerminalViewOptions,
): Promise<TerminalView> {
  const host = document.createElement("div");
  host.style.height = "100%";
  container.appendChild(host);
  try {
    const view = renderer === "ghostty" ? await openGhostty(host, opts) : openXTerm(host, opts);
    // Both renderers cache glyph measurements, so the icon font has to be
    // loaded before it joins the stack; otherwise icons are measured as
    // fallback glyphs (in xterm's DOM renderer they also push the rest of
    // their row off the cell grid).
    void document.fonts.load(`${opts.fontSize}px ${ICON_FONT}`, "\ue0a0").then(() => {
      if (!host.isConnected) return;
      view.setFontFamily(`${FONT_FAMILY}, ${ICON_FONT}`);
      opts.onCellSizeChange();
    });
    return view;
  } catch (err) {
    host.remove();
    throw err;
  }
}

interface RendererView extends TerminalView {
  setFontFamily(fontFamily: string): void;
}

// The shortcut row supplies normal cursor-key sequences. Full-screen apps
// can request application cursor mode; modified keys retain their CSI form.
function shortcutInput(data: string, applicationCursor: boolean): string {
  if (
    applicationCursor &&
    data.length === 3 &&
    data.startsWith("\x1b[") &&
    "ABCDHF".includes(data[2])
  ) {
    return "\x1bO" + data[2];
  }
  return data;
}

function bufferLines(
  buffer: {
    length: number;
    getLine(y: number): { translateToString(trim: boolean): string } | undefined;
  },
  start: number,
  count: number,
): string[] {
  const lines: string[] = [];
  for (let y = Math.max(0, start); y < Math.min(buffer.length, start + count); y++) {
    lines.push(buffer.getLine(y)?.translateToString(true) ?? "");
  }
  return lines;
}

function attachTouch(
  host: HTMLElement,
  focus: () => void,
  ownsScrolling: boolean,
  fontSize: () => number,
  setFontSize: (size: number) => void,
) {
  return attachTerminalTouch(host, {
    focus,
    ownsScrolling: () => ownsScrolling,
    fontSize,
    setFontSize,
    // Feed a drag back in as wheel events, so it takes the same path as the
    // mouse wheel: scrollback, or the program's own scrolling in a TUI.
    scrollBy: (deltaY) => {
      host.dispatchEvent(
        new WheelEvent("wheel", { deltaY, deltaMode: 0, bubbles: true, cancelable: true }),
      );
    },
  });
}

function openXTerm(host: HTMLElement, opts: TerminalViewOptions): RendererView {
  const term = new XTerm({
    cols: opts.cols,
    rows: opts.rows,
    cursorBlink: true,
    fontSize: opts.fontSize,
    fontFamily: FONT_FAMILY,
    theme: getTerminalTheme(opts.isDark),
    scrollback: 10000,
    // Kitty keyboard protocol off. When xterm.js offers it, nvim turns it on,
    // and xterm.js then sends macOS Cmd shortcuts to the app as keys (Cmd+V
    // arrives as `<D-v>` instead of pasting).
    vtExtensions: { kittyKeyboard: false },
  } as ConstructorParameters<typeof XTerm>[0]);

  // Ensure control key combinations (like Ctrl-B for tmux) are passed
  // through to the terminal and not intercepted by the browser.
  term.attachCustomKeyEventHandler((e: KeyboardEvent) => {
    // Allow Ctrl+Shift+C / Ctrl+Shift+V for copy/paste
    if (e.ctrlKey && e.shiftKey && (e.key === "C" || e.key === "V")) {
      return false; // Let browser handle it
    }
    // For all Ctrl+<key> combos (e.g. Ctrl-B for tmux prefix),
    // prevent the browser default and let xterm handle it.
    if (e.ctrlKey && !e.shiftKey && !e.altKey && !e.metaKey && e.type === "keydown") {
      e.preventDefault();
      return true; // Let xterm process it
    }
    return true;
  });
  term.onData(opts.onData);

  const fit = new XTermFitAddon();
  term.loadAddon(fit);
  term.loadAddon(new WebLinksAddon());
  term.open(host);
  loadWebgl(term, opts);
  const detachTouch = attachTouch(
    host,
    () => term.focus(),
    false,
    () => term.options.fontSize!,
    opts.onFontSizeChange,
  );
  let scrollFrame = 0;
  let followResize = false;
  term.onRender(() => {
    if (!followResize) return;
    followResize = false;
    cancelAnimationFrame(scrollFrame);
    // After a hidden tab resizes, xterm's scrollbar can already be at the
    // bottom while its buffer still shows the old viewport. Move it one row
    // and back after rendering to synchronize them without a visible jump.
    scrollFrame = requestAnimationFrame(() => {
      term.scrollLines(-1);
      term.scrollLines(term.buffer.active.length);
    });
  });

  return {
    get cols() {
      return term.cols;
    },
    get rows() {
      return term.rows;
    },
    write: (data) => term.write(data),
    focus: () => term.focus(),
    paste: (data) => term.paste(data),
    input: (data) => term.input(shortcutInput(data, term.modes.applicationCursorKeysMode), true),
    setFontSize: (size) => (term.options.fontSize = size),
    fit: () => {
      if (!host.clientWidth || !host.clientHeight) return;
      const atBottom = term.buffer.active.viewportY === term.buffer.active.baseY;
      const { cols, rows } = term;
      fit.fit();
      if (atBottom && (cols !== term.cols || rows !== term.rows)) followResize = true;
    },
    lines: (which) => {
      const buffer = term.buffer.active;
      return which === "screen"
        ? bufferLines(buffer, buffer.viewportY, term.rows)
        : bufferLines(buffer, 0, buffer.length);
    },
    setTheme: (isDark) => {
      term.options.theme = getTerminalTheme(isDark);
      return true;
    },
    setFontFamily: (fontFamily) => (term.options.fontFamily = fontFamily),
    dispose: () => {
      cancelAnimationFrame(scrollFrame);
      detachTouch();
      term.dispose();
      host.remove();
    },
  };
}

// The WebGL renderer draws box-drawing, block and powerline characters itself,
// filling the cell, so powerline status bars join up seamlessly; the DOM
// renderer can only use the font's glyphs. Without WebGL2 the terminal stays
// on the DOM renderer. It also returns to it when a lost context isn't
// restored (browsers cap live contexts, about 16 in Chrome, and drop the
// oldest), and then refits, because the DOM renderer's cells differ in width.
function loadWebgl(term: XTerm, opts: TerminalViewOptions) {
  try {
    const webgl = new WebglAddon();
    webgl.onContextLoss(() => {
      webgl.dispose();
      opts.onCellSizeChange();
    });
    term.loadAddon(webgl);
  } catch (err) {
    console.warn("terminal: WebGL2 unavailable, using the DOM renderer", err);
  }
}

type GhosttyModule = typeof GhosttyWeb;
let ghosttyModule: Promise<GhosttyModule> | null = null;

/**
 * ghostty-web, and an emulator for one terminal. Terminals sharing an
 * emulator reuse each other's freed memory without clearing it, so a new
 * terminal would show text from a closed one.
 */
async function loadGhostty() {
  // eslint-disable-next-line @typescript-eslint/ban-ts-comment
  // @ts-ignore - runtime URL, built by scripts/build.js
  ghosttyModule ??= (import("/ghostty-web.js") as Promise<GhosttyModule>).catch((err) => {
    ghosttyModule = null; // let the next terminal try again
    throw err;
  });
  const mod = await ghosttyModule;
  return { mod, ghostty: await mod.Ghostty.load() };
}

async function openGhostty(host: HTMLElement, opts: TerminalViewOptions): Promise<RendererView> {
  const { mod, ghostty } = await loadGhostty();
  const term = new mod.Terminal({
    ghostty,
    cols: opts.cols,
    rows: opts.rows,
    cursorBlink: true,
    fontSize: opts.fontSize,
    fontFamily: FONT_FAMILY,
    theme: getTerminalTheme(opts.isDark),
    // ghostty's limit is in bytes, about 14 per cell: room for xterm.js's
    // 10,000 lines at up to 180 columns.
    scrollback: 10_000 * 180 * 14,
    // Scroll at once, as xterm.js does. The animation heads for a target
    // that output and input don't update, so it would scroll away again.
    smoothScrollDuration: 0,
  });

  // ghostty-web's handler returns true for keys it should leave alone, and
  // then also cancels the browser's default action. Cmd+V, Ctrl+Shift+V and
  // Cmd+C it already leaves to the browser's paste and its own copy.
  term.attachCustomKeyEventHandler((e) => {
    if (e.type !== "keydown") return false;
    // Copy, as in xterm.js. The program must not see it: it encodes as ^C.
    if (e.ctrlKey && e.shiftKey && e.code === "KeyC") {
      if (term.hasSelection()) void navigator.clipboard.writeText(term.getSelection());
      return true;
    }
    // ghostty-web pastes on Ctrl+V. Send ^V to the program as xterm.js does
    // (vim's visual block, bash's quoted insert); Ctrl+Shift+V pastes.
    if (e.ctrlKey && !e.shiftKey && !e.altKey && !e.metaKey && e.code === "KeyV") {
      term.input("\x16", true);
      return true;
    }
    return false;
  });
  // Input returns a scrolled-back view to the bottom, as in xterm.js. Replies
  // the terminal makes to output arrive while writing, and don't.
  let writing = false;
  term.onData((data) => {
    if (!writing) term.scrollToBottom();
    opts.onData(data);
  });

  const fit = new mod.FitAddon();
  term.loadAddon(fit);
  // open() ends by focusing the terminal, which would take focus from the
  // composer whenever a terminal opens or reopens (theme switch). Focusing is
  // focus()'s job.
  term.focus = () => {};
  // open() adds document listeners that dispose() leaves behind, holding on
  // to the whole emulator. Note them, to remove them with it.
  const documentListeners: Parameters<Document["removeEventListener"]>[] = [];
  document.addEventListener = (...args: Parameters<Document["addEventListener"]>) => {
    documentListeners.push(args);
    Document.prototype.addEventListener.apply(document, args);
  };
  try {
    term.open(host);
  } finally {
    Reflect.deleteProperty(document, "addEventListener");
    Reflect.deleteProperty(term, "focus");
  }
  // open() makes the host contenteditable, and the browser's caret would
  // blink over the canvas next to the terminal's own cursor.
  host.style.caretColor = "transparent";
  // It also labels the host a "Terminal input" textbox, beside the textarea
  // that really takes input (see installGhosttyInputFocus). Keep just one.
  host.removeAttribute("role");
  host.removeAttribute("aria-label");
  installGhosttyWheelPolicy(term as unknown as GhosttyWheelTerminal);
  const removeSyncOutput = installGhosttySynchronizedOutput(
    term as unknown as GhosttySynchronizedTerminal,
  );
  const removeInputFocus = installGhosttyInputFocus(host);
  const removeSelectionPolicy = installGhosttySelectionPolicy(
    term as unknown as GhosttyMouseTerminal,
  );
  installGhosttyMetaPassthrough(term as unknown as GhosttyKeyTerminal);
  installGhosttyWrappedUrls(term as unknown as GhosttyLinkTerminal, mod.UrlRegexProvider);
  // Focus the textarea directly: Terminal.focus() reaches it only after a
  // timeout, outside the tap that has to open a soft keyboard.
  const input = host.querySelector("textarea")!;
  const focus = () => input.focus({ preventScroll: true });
  const detachTouch = attachTouch(
    host,
    focus,
    true,
    () => term.options.fontSize!,
    opts.onFontSizeChange,
  );

  return {
    get cols() {
      return term.cols;
    },
    get rows() {
      return term.rows;
    },
    write: (data) => {
      writing = true;
      try {
        writeKeepingView(term, data);
      } finally {
        writing = false;
      }
    },
    focus,
    paste: (data) => term.paste(data),
    input: (data) => term.input(shortcutInput(data, term.getMode(1, false)), true),
    setFontSize: (size) => (term.options.fontSize = size),
    // FitAddon.fit() ignores calls for 50ms after a resize, so the last
    // resize of a burst (a divider drag, a keyboard sliding in) was lost.
    fit: () => {
      const size = fit.proposeDimensions();
      if (size && (size.cols !== term.cols || size.rows !== term.rows)) {
        term.resize(size.cols, size.rows);
      }
    },
    lines: (which) => {
      const wasm = wasmTerminal(term);
      const scrollback = term.buffer.active.type === "alternate" ? 0 : wasm.getScrollbackLength();
      if (which === "all") return ghosttyLines(wasm, scrollback, 0, scrollback + term.rows);
      // getViewportY() counts rows scrolled back from the bottom.
      const top = scrollback - Math.floor(term.getViewportY());
      return ghosttyLines(wasm, scrollback, top, term.rows);
    },
    // ghostty-web builds its palette into the emulator at open().
    setTheme: () => false,
    setFontFamily: (fontFamily) => (term.options.fontFamily = fontFamily),
    dispose: () => {
      detachTouch();
      removeInputFocus();
      removeSelectionPolicy();
      removeSyncOutput();
      term.dispose();
      for (const args of documentListeners) document.removeEventListener(...args);
      host.remove();
    },
  };
}

/**
 * Write to a ghostty terminal without moving a scrolled-back view off the
 * lines it shows, as xterm.js does. write() jumps to the bottom.
 */
function writeKeepingView(term: GhosttyWeb.Terminal, data: Uint8Array | string) {
  // Rows scrolled back from the bottom, fractional after a pixel wheel.
  const back = term.getViewportY();
  if (back <= 0) return term.write(data);
  const wasm = wasmTerminal(term);
  const before = wasm.getScrollbackLength();
  // The screen's top row, indexed from the oldest history row.
  const top = before - Math.floor(back);
  const viewed = ghosttyLines(wasm, before, top, term.rows);
  const oldest = ghosttyLines(wasm, before, 0, OLDEST_ROWS);
  term.write(data);
  if (term.buffer.active.type === "alternate") return;
  const grown = wasm.getScrollbackLength() - before;
  const evicted = evictedRows(wasm, { viewed, top, oldest }, grown, data.length);
  term.scrollLines(-(back + grown + evicted));
}

/** History rows compared to tell an eviction from a redraw. */
const OLDEST_ROWS = 8;

/**
 * How far rows that were on screen at `top` moved up during a write. History
 * rows keep their index unless the oldest are evicted to make room. Each row
 * the write added took at least one character of it, which bounds the
 * eviction; 0 if the rows can't be found, e.g. because output changed them.
 */
function evictedRows(
  wasm: GhosttyWasmTerminal,
  before: { viewed: string[]; top: number; oldest: string[] },
  grown: number,
  written: number,
): number {
  const { viewed, top, oldest } = before;
  const scrollback = wasm.getScrollbackLength();
  const rows = (row: number, count: number) =>
    ghosttyLines(wasm, scrollback, row, count).join("\n");
  const text = viewed.join("\n");
  if (rows(top, viewed.length) === text) return 0;
  // No growth and the same oldest rows: the program redrew the screen in
  // place, as progress bars do, often; skip the slow search. Output adding
  // exactly the rows it evicts, over history that repeats, looks the same
  // and moves the view.
  if (grown === 0 && rows(0, oldest.length) === oldest.join("\n")) return 0;
  // Test candidates on the row least likely to repeat first.
  const probe = viewed.reduce((best, line, i) => (line.length > viewed[best].length ? i : best), 0);
  const most = Math.min(top, written - grown);
  for (let evicted = Math.max(1, -grown); evicted <= most; evicted++) {
    const row = top - evicted;
    const line = ghosttyLines(wasm, scrollback, row + probe, 1)[0];
    if (line === viewed[probe] && rows(row, viewed.length) === text) return evicted;
  }
  return 0;
}

function wasmTerminal(term: GhosttyWeb.Terminal): GhosttyWasmTerminal {
  // Replaced by reset(), so not to be kept.
  return (term as unknown as { wasmTerm: GhosttyWasmTerminal }).wasmTerm;
}

// The emulator behind ghostty-web's Terminal. Its xterm.js-style
// buffer.getLine() drops empty cells and all but the first code point of a
// cluster, so text is read from here, as ghostty-web's getSelection() does.
interface GhosttyCell {
  codepoint: number; // 0 for an empty cell
  width: number; // 0 for the second half of a wide character
  grapheme_len: number; // code points beyond the first
}

interface GhosttyWasmTerminal {
  getScrollbackLength(): number;
  getScrollbackLine(row: number): GhosttyCell[] | null;
  getLine(row: number): GhosttyCell[] | null;
  getScrollbackGraphemeString(row: number, col: number): string;
  getGraphemeString(row: number, col: number): string;
}

/** Rows from `top`, counting scrollback rows first, then the screen's. */
function ghosttyLines(
  wasm: GhosttyWasmTerminal,
  scrollback: number,
  top: number,
  count: number,
): string[] {
  const lines: string[] = [];
  for (let y = Math.max(0, top); y < top + count; y++) {
    const inScrollback = y < scrollback;
    const row = inScrollback ? y : y - scrollback;
    const cells = inScrollback ? wasm.getScrollbackLine(row) : wasm.getLine(row);
    if (!cells) continue;
    let text = "";
    cells.forEach((cell, col) => {
      if (cell.width === 0) return;
      if (cell.codepoint === 0) text += " ";
      else if (cell.grapheme_len === 0) text += String.fromCodePoint(cell.codepoint);
      else if (inScrollback) text += wasm.getScrollbackGraphemeString(row, col);
      else text += wasm.getGraphemeString(row, col);
    });
    lines.push(text.trimEnd());
  }
  return lines;
}
