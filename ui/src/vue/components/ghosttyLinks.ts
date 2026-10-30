// URLs that soft-wrap over several rows, for ghostty-web@0.4.0-next.14.
//
// ghostty-web's UrlRegexProvider finds URLs one row at a time. A URL longer
// than the terminal is wide (an OAuth sign-in link) wraps, and a Ctrl/Cmd+click
// on its first row opened just that row's part of it, and on the other rows
// opened nothing. xterm.js's WebLinksAddon joins wrapped rows; so does this,
// in a provider that takes the built-in one's place. Its LinkDetector and
// renderer already handle links that span rows.

/** URL schemes and characters, as ghostty-web's UrlRegexProvider has them. */
const URL_REGEX =
  /(?:https?:\/\/|mailto:|ftp:\/\/|ssh:\/\/|git:\/\/|tel:|magnet:|gemini:\/\/|gopher:\/\/|news:)[\w\-.~:/?#@!$&*+,;=%]+/gi;
const URL_CHAR = /^[\w\-.~:/?#@!$&*+,;=%]$/;
const TRAILING_PUNCTUATION = /[.,;!?)\]]+$/;

/** How many rows a URL may wrap over, either way from the one asked about. */
const MAX_ROWS = 64;

/** One terminal row, as the URL finder reads it. */
export interface UrlRow {
  /** Each cell's text: " " when empty, "" for the second half of a wide one. */
  cells: string[];
  /**
   * Whether the row carries on the one above, which soft-wrapped onto it.
   * Undefined when the emulator doesn't know (ghostty-web drops the flag
   * from scrollback rows); then it does if the row above is full.
   */
  continues?: boolean;
}

export interface CellPosition {
  x: number;
  y: number;
}

export interface FoundUrl {
  url: string;
  start: CellPosition;
  end: CellPosition;
}

function joined(upper: UrlRow, lower: UrlRow): boolean {
  return lower.continues ?? (upper.cells.length > 0 && upper.cells[upper.cells.length - 1] !== " ");
}

function isUrlChar(cell: string | undefined): boolean {
  return cell !== undefined && (cell === "" || URL_CHAR.test(cell));
}

/** The cell a URL crossing into or out of `row` would have to begin or end with. */
function edge(row: UrlRow, at: "first" | "last"): string | undefined {
  const cells = row.cells.filter((c) => c !== "");
  return at === "first" ? cells[0] : cells[cells.length - 1];
}

/**
 * The URLs on row `y`, with the rows they wrap onto. `row` reads the buffer;
 * it returns undefined past either end.
 */
export function urlsOnRow(y: number, row: (y: number) => UrlRow | undefined): FoundUrl[] {
  const here = row(y);
  if (!here) return [];
  const rows = new Map<number, UrlRow>([[y, here]]);
  // A URL reaching row y from above covers every row between, and the
  // start of row y; one leaving it covers the end of row y and every row
  // until it ends. So only rows made of URL characters lead further.
  let top = y;
  while (top > y - MAX_ROWS) {
    const current = rows.get(top)!;
    if (current.continues === false) break;
    const leads = top === y ? isUrlChar(edge(current, "first")) : current.cells.every(isUrlChar);
    const above = leads ? row(top - 1) : undefined;
    if (!above || !joined(above, current)) break;
    rows.set(--top, above);
  }
  let bottom = y;
  while (bottom < y + MAX_ROWS) {
    const current = rows.get(bottom)!;
    const leads = bottom === y ? isUrlChar(edge(current, "last")) : current.cells.every(isUrlChar);
    const below = leads ? row(bottom + 1) : undefined;
    if (!below || !joined(current, below)) break;
    rows.set(++bottom, below);
  }

  // The joined rows' text, and the cell each UTF-16 code unit came from.
  let text = "";
  const at: CellPosition[] = [];
  for (let r = top; r <= bottom; r++) {
    rows.get(r)!.cells.forEach((cell, x) => {
      text += cell;
      for (let i = 0; i < cell.length; i++) at.push({ x, y: r });
    });
  }

  const found: FoundUrl[] = [];
  for (const match of text.matchAll(URL_REGEX)) {
    const url = match[0].replace(TRAILING_PUNCTUATION, "");
    // Just a scheme. ghostty-web skips these too.
    if (url.length <= 8) continue;
    const start = at[match.index];
    const end = at[match.index + url.length - 1];
    if (start.y <= y && end.y >= y) found.push({ url, start, end });
  }
  return found;
}

// The ghostty-web internals the provider reads. Not public API, but
// registerLinkProvider().
interface GhosttyCell {
  codepoint: number; // 0 for an empty cell
  width: number; // 0 for the second half of a wide character
  grapheme_len: number; // code points beyond the first
  hyperlink_id: number; // nonzero in an OSC 8 hyperlink
}

export interface GhosttyLinkTerminal {
  readonly cols: number;
  readonly rows: number;
  buffer: { active: { type: string } };
  wasmTerm?: {
    getScrollbackLength(): number;
    getScrollbackLine(row: number): GhosttyCell[] | null;
    /** Brings the screen getViewport() reads up to date. */
    update(): void;
    /**
     * The active screen, row after row. A pool it reuses on every call, so
     * only good until the next.
     */
    getViewport(): GhosttyCell[];
    /** Whether the active screen's row carries on the one above. */
    isRowWrapped(row: number): boolean;
    getScrollbackGraphemeString(row: number, col: number): string;
    getGraphemeString(row: number, col: number): string;
  };
  linkDetector?: { providers?: unknown[]; invalidateCache(): void };
  onResize(listener: () => void): unknown;
  registerLinkProvider(provider: {
    provideLinks(y: number, callback: (links: GhosttyLink[] | undefined) => void): void;
  }): void;
}

interface GhosttyLink {
  text: string;
  range: { start: CellPosition; end: CellPosition };
  activate(event: MouseEvent): void;
}

/**
 * A reader of the active buffer's rows, numbered as ghostty-web's
 * buffer.getLine() does, for one synchronous provideLinks() call. Its
 * getLine() reads the whole screen for each row, so this reads it once.
 */
function ghosttyRows(term: GhosttyLinkTerminal): (y: number) => UrlRow | undefined {
  const wasm = term.wasmTerm;
  if (!wasm) return () => undefined;
  const scrollback = term.buffer.active.type === "alternate" ? 0 : wasm.getScrollbackLength();
  const { cols, rows } = term;
  let screen: GhosttyCell[] | undefined;
  return (y) => {
    if (y < 0 || y >= scrollback + rows) return undefined;
    const inScrollback = y < scrollback;
    const r = inScrollback ? y : y - scrollback;
    if (!inScrollback && !screen) {
      wasm.update();
      screen = wasm.getViewport();
    }
    const cells = inScrollback
      ? wasm.getScrollbackLine(r)
      : screen!.slice(r * cols, (r + 1) * cols);
    if (!cells) return undefined;
    return {
      cells: cells.map((cell, x) => {
        if (cell.width === 0) return "";
        // OSC 8 hyperlinks are the OSC 8 provider's, even where their text
        // looks like a URL. A link cached here would win on rows that
        // provider hasn't been asked about yet.
        if (cell.codepoint === 0 || cell.hyperlink_id !== 0) return " ";
        if (cell.grapheme_len === 0) return String.fromCodePoint(cell.codepoint);
        return inScrollback ? wasm.getScrollbackGraphemeString(r, x) : wasm.getGraphemeString(r, x);
      }),
      continues: inScrollback ? undefined : wasm.isRowWrapped(r),
    };
  };
}

/** ghostty-web's UrlRegexProvider class. */
export type UrlRegexProviderCtor = abstract new (...args: never[]) => unknown;

/**
 * Replace ghostty-web's row-at-a-time URL provider with one that follows soft
 * wraps. `UrlRegexProvider` is ghostty-web's export of the built-in one. Links
 * open on Ctrl/Cmd+click, as ghostty-web's do.
 */
export function installGhosttyWrappedUrls(
  term: GhosttyLinkTerminal,
  UrlRegexProvider: UrlRegexProviderCtor,
) {
  const providers = term.linkDetector?.providers;
  const builtIn = providers?.findIndex((p) => p instanceof UrlRegexProvider) ?? -1;
  if (!providers || builtIn < 0) throw new Error("ghostty-web has no URL link provider");
  providers.splice(builtIn, 1);
  // ghostty-web forgets the links it found when output arrives, but not when
  // a resize reflows the text under them.
  term.onResize(() => term.linkDetector?.invalidateCache());
  term.registerLinkProvider({
    provideLinks(y, callback) {
      const links = urlsOnRow(y, ghosttyRows(term)).map(
        ({ url, start, end }): GhosttyLink => ({
          text: url,
          range: { start, end },
          activate(event) {
            if (event.ctrlKey || event.metaKey) window.open(url, "_blank", "noopener,noreferrer");
          },
        }),
      );
      callback(links.length > 0 ? links : undefined);
    },
  });
}
