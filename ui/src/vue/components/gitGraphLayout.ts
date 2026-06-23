// Framework-agnostic helpers for the Vue GitGraphViewer port. These mirror the
// module-scope helpers in components/GitGraphViewer.tsx verbatim (lane layout,
// MD5/gravatar, clipboard, relative-time formatting).
import type { GitGraphCommit } from "../../types";

// Lane colors cycle by lane index (stable per lane, GitX-style).
export const LANE_COLORS = [
  "#e6194B",
  "#3cb44b",
  "#4363d8",
  "#f58231",
  "#911eb4",
  "#42d4f4",
  "#f032e6",
  "#9A6324",
  "#469990",
  "#bfef45",
];

export function laneColor(i: number): string {
  return LANE_COLORS[((i % LANE_COLORS.length) + LANE_COLORS.length) % LANE_COLORS.length];
}

export function normalizeCommits(commits: GitGraphCommit[]): GitGraphCommit[] {
  return commits.map((c) => ({
    ...c,
    parents: c.parents ?? [],
    refs: c.refs ?? [],
  }));
}

// Line segment within a single row.
//   upper=true  : top half of the cell (connects previous row to this row)
//   upper=false : bottom half (connects this row to the next row)
// 'from' and 'to' are 1-based column positions.
export interface GraphLine {
  upper: boolean;
  from: number;
  to: number;
  colorIndex: number;
}

export interface RowInfo {
  col: number; // 1-based column of the commit dot
  colorIndex: number; // lane color for the dot itself
  lines: GraphLine[];
  numColumns: number; // number of active lanes after laying out this row (used for width)
}

// Ported from GitX's PBGitGrapher. Compacts lanes as they die, so columns
// reflow left to fill gaps; colors are assigned per-lane (stable across rows).
export function computeLayout(commits: GitGraphCommit[]): { rows: RowInfo[]; maxColumns: number } {
  type Lane = { index: number; sha: string | null } | null;
  let lanes: Lane[] = [];
  let nextLaneIndex = 0;
  const rows: RowInfo[] = [];
  let maxColumns = 0;

  for (const commit of commits) {
    const previousLanes = lanes;
    const currentLanes: Lane[] = [];
    const lines: GraphLine[] = [];
    const parents = commit.parents;
    const parentCount = parents.length;

    let newPosition = -1;
    let currentLane: Lane = null;
    let didProcessFirstParent = false;

    // Walk previous lanes, compacting into currentLanes.
    let columnIndex = 0; // 1-based (pre-increment)
    for (const laneCandidate of previousLanes) {
      columnIndex++;
      if (laneCandidate === null) continue;

      const lane = laneCandidate;
      if (lane.sha === commit.hash) {
        if (!didProcessFirstParent) {
          didProcessFirstParent = true;
          currentLanes.push(lane);
          currentLane = lane;
          newPosition = currentLanes.length;

          lines.push({
            upper: true,
            from: columnIndex,
            to: newPosition,
            colorIndex: lane.index,
          });
          if (parentCount > 0) {
            lines.push({
              upper: false,
              from: newPosition,
              to: newPosition,
              colorIndex: lane.index,
            });
          }
        } else {
          // Merge incoming: upper half from its previous column to the dot.
          lines.push({
            upper: true,
            from: columnIndex,
            to: newPosition,
            colorIndex: lane.index,
          });
        }
      } else {
        // Carry the lane forward; may shift leftward as gaps compact.
        currentLanes.push(lane);
        const lanePosition = currentLanes.length;
        lines.push({
          upper: true,
          from: columnIndex,
          to: lanePosition,
          colorIndex: lane.index,
        });
        lines.push({
          upper: false,
          from: lanePosition,
          to: lanePosition,
          colorIndex: lane.index,
        });
      }
    }

    // Commit not on any existing lane: introduce a fresh lane for its first parent.
    if (!didProcessFirstParent && parentCount > 0) {
      const parentSHA = parents[0];
      const newLane: Lane = { index: nextLaneIndex++, sha: parentSHA };
      currentLanes.push(newLane);
      newPosition = currentLanes.length;
      currentLane = newLane;
      lines.push({
        upper: false,
        from: newPosition,
        to: newPosition,
        colorIndex: newLane.index,
      });
    } else if (!didProcessFirstParent && parentCount === 0) {
      // Root commit with no existing lane: give it its own column.
      const newLane: Lane = { index: nextLaneIndex++, sha: null };
      currentLanes.push(newLane);
      newPosition = currentLanes.length;
      currentLane = newLane;
    }

    // Extra parents (merge commits): draw an outgoing branch for each.
    let addedParent = false;
    if (parentCount > 1) {
      for (let pi = 1; pi < parentCount; pi++) {
        const parentSHA = parents[pi];
        let lanePosition = 0;
        let alreadyDisplayed = false;
        for (const laneCandidate of currentLanes) {
          lanePosition++;
          if (laneCandidate === null) continue;
          if (laneCandidate.sha === parentSHA) {
            lines.push({
              upper: false,
              from: lanePosition,
              to: newPosition,
              colorIndex: laneCandidate.index,
            });
            alreadyDisplayed = true;
            break;
          }
        }
        if (alreadyDisplayed) continue;
        addedParent = true;
        const newLane: Lane = { index: nextLaneIndex++, sha: parentSHA };
        currentLanes.push(newLane);
        const lanePositionForNewLane = currentLanes.length;
        lines.push({
          upper: false,
          from: lanePositionForNewLane,
          to: newPosition,
          colorIndex: newLane.index,
        });
      }
    }

    // Lane follows the first parent into the next row; lane dies if none.
    if (currentLane) {
      const firstParent = parents[0];
      if (firstParent) {
        currentLane.sha = firstParent;
      } else if (parentCount === 0) {
        const slot = currentLanes.indexOf(currentLane);
        if (slot >= 0) currentLanes[slot] = null;
      }
    }

    const numColumns = addedParent ? currentLanes.length - 1 : currentLanes.length;
    if (numColumns > maxColumns) maxColumns = numColumns;
    rows.push({
      col: newPosition,
      colorIndex: currentLane ? currentLane.index : 0,
      lines,
      numColumns,
    });
    lanes = currentLanes;
  }

  return { rows, maxColumns };
}

export const ROW_H = 24;
export const LANE_W = 14;
export const LEFT_PAD = 10;
export const DOT_R = 4;

export function colX(col: number): number {
  // col is 1-based.
  return LEFT_PAD + (col - 1) * LANE_W;
}

export function formatRelative(ts: number): string {
  const diff = Date.now() / 1000 - ts;
  if (diff < 60) return "just now";
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  if (diff < 86400 * 30) return `${Math.floor(diff / 86400)}d ago`;
  const d = new Date(ts * 1000);
  return d.toLocaleDateString();
}

// MD5 for Gravatar URLs. Gravatar hashes are MD5 of the trimmed,
// lowercased email — there's no native crypto API for MD5 in browsers,
// so we carry a tiny implementation. Returns a 32-char lowercase hex string.
export function md5(input: string): string {
  // Public-domain MD5 implementation adapted for UTF-8 strings. Intentionally
  // compact; not meant for security uses.
  function toBytes(str: string): number[] {
    const utf8 = unescape(encodeURIComponent(str));
    const out: number[] = [];
    for (let i = 0; i < utf8.length; i++) out.push(utf8.charCodeAt(i));
    return out;
  }
  function add32(a: number, b: number): number {
    return (a + b) & 0xffffffff;
  }
  function rol(x: number, n: number): number {
    return (x << n) | (x >>> (32 - n));
  }
  function cmn(q: number, a: number, b: number, x: number, s: number, t: number): number {
    return add32(rol(add32(add32(a, q), add32(x, t)), s), b);
  }
  function ff(a: number, b: number, c: number, d: number, x: number, s: number, t: number): number {
    return cmn((b & c) | (~b & d), a, b, x, s, t);
  }
  function gg(a: number, b: number, c: number, d: number, x: number, s: number, t: number): number {
    return cmn((b & d) | (c & ~d), a, b, x, s, t);
  }
  function hh(a: number, b: number, c: number, d: number, x: number, s: number, t: number): number {
    return cmn(b ^ c ^ d, a, b, x, s, t);
  }
  function ii(a: number, b: number, c: number, d: number, x: number, s: number, t: number): number {
    return cmn(c ^ (b | ~d), a, b, x, s, t);
  }

  const bytes = toBytes(input);
  const n = bytes.length;
  const nBits = n * 8;
  const padLen = ((n + 8) >>> 6) + 1;
  const words = new Array<number>(padLen * 16).fill(0);
  for (let i = 0; i < n; i++) {
    words[i >> 2] |= bytes[i] << ((i % 4) * 8);
  }
  words[n >> 2] |= 0x80 << ((n % 4) * 8);
  words[padLen * 16 - 2] = nBits;

  let a = 0x67452301,
    b = 0xefcdab89,
    c = 0x98badcfe,
    d = 0x10325476;

  for (let i = 0; i < words.length; i += 16) {
    const oa = a,
      ob = b,
      oc = c,
      od = d;
    a = ff(a, b, c, d, words[i + 0], 7, -680876936);
    d = ff(d, a, b, c, words[i + 1], 12, -389564586);
    c = ff(c, d, a, b, words[i + 2], 17, 606105819);
    b = ff(b, c, d, a, words[i + 3], 22, -1044525330);
    a = ff(a, b, c, d, words[i + 4], 7, -176418897);
    d = ff(d, a, b, c, words[i + 5], 12, 1200080426);
    c = ff(c, d, a, b, words[i + 6], 17, -1473231341);
    b = ff(b, c, d, a, words[i + 7], 22, -45705983);
    a = ff(a, b, c, d, words[i + 8], 7, 1770035416);
    d = ff(d, a, b, c, words[i + 9], 12, -1958414417);
    c = ff(c, d, a, b, words[i + 10], 17, -42063);
    b = ff(b, c, d, a, words[i + 11], 22, -1990404162);
    a = ff(a, b, c, d, words[i + 12], 7, 1804603682);
    d = ff(d, a, b, c, words[i + 13], 12, -40341101);
    c = ff(c, d, a, b, words[i + 14], 17, -1502002290);
    b = ff(b, c, d, a, words[i + 15], 22, 1236535329);

    a = gg(a, b, c, d, words[i + 1], 5, -165796510);
    d = gg(d, a, b, c, words[i + 6], 9, -1069501632);
    c = gg(c, d, a, b, words[i + 11], 14, 643717713);
    b = gg(b, c, d, a, words[i + 0], 20, -373897302);
    a = gg(a, b, c, d, words[i + 5], 5, -701558691);
    d = gg(d, a, b, c, words[i + 10], 9, 38016083);
    c = gg(c, d, a, b, words[i + 15], 14, -660478335);
    b = gg(b, c, d, a, words[i + 4], 20, -405537848);
    a = gg(a, b, c, d, words[i + 9], 5, 568446438);
    d = gg(d, a, b, c, words[i + 14], 9, -1019803690);
    c = gg(c, d, a, b, words[i + 3], 14, -187363961);
    b = gg(b, c, d, a, words[i + 8], 20, 1163531501);
    a = gg(a, b, c, d, words[i + 13], 5, -1444681467);
    d = gg(d, a, b, c, words[i + 2], 9, -51403784);
    c = gg(c, d, a, b, words[i + 7], 14, 1735328473);
    b = gg(b, c, d, a, words[i + 12], 20, -1926607734);

    a = hh(a, b, c, d, words[i + 5], 4, -378558);
    d = hh(d, a, b, c, words[i + 8], 11, -2022574463);
    c = hh(c, d, a, b, words[i + 11], 16, 1839030562);
    b = hh(b, c, d, a, words[i + 14], 23, -35309556);
    a = hh(a, b, c, d, words[i + 1], 4, -1530992060);
    d = hh(d, a, b, c, words[i + 4], 11, 1272893353);
    c = hh(c, d, a, b, words[i + 7], 16, -155497632);
    b = hh(b, c, d, a, words[i + 10], 23, -1094730640);
    a = hh(a, b, c, d, words[i + 13], 4, 681279174);
    d = hh(d, a, b, c, words[i + 0], 11, -358537222);
    c = hh(c, d, a, b, words[i + 3], 16, -722521979);
    b = hh(b, c, d, a, words[i + 6], 23, 76029189);
    a = hh(a, b, c, d, words[i + 9], 4, -640364487);
    d = hh(d, a, b, c, words[i + 12], 11, -421815835);
    c = hh(c, d, a, b, words[i + 15], 16, 530742520);
    b = hh(b, c, d, a, words[i + 2], 23, -995338651);

    a = ii(a, b, c, d, words[i + 0], 6, -198630844);
    d = ii(d, a, b, c, words[i + 7], 10, 1126891415);
    c = ii(c, d, a, b, words[i + 14], 15, -1416354905);
    b = ii(b, c, d, a, words[i + 5], 21, -57434055);
    a = ii(a, b, c, d, words[i + 12], 6, 1700485571);
    d = ii(d, a, b, c, words[i + 3], 10, -1894986606);
    c = ii(c, d, a, b, words[i + 10], 15, -1051523);
    b = ii(b, c, d, a, words[i + 1], 21, -2054922799);
    a = ii(a, b, c, d, words[i + 8], 6, 1873313359);
    d = ii(d, a, b, c, words[i + 15], 10, -30611744);
    c = ii(c, d, a, b, words[i + 6], 15, -1560198380);
    b = ii(b, c, d, a, words[i + 13], 21, 1309151649);
    a = ii(a, b, c, d, words[i + 4], 6, -145523070);
    d = ii(d, a, b, c, words[i + 11], 10, -1120210379);
    c = ii(c, d, a, b, words[i + 2], 15, 718787259);
    b = ii(b, c, d, a, words[i + 9], 21, -343485551);

    a = add32(a, oa);
    b = add32(b, ob);
    c = add32(c, oc);
    d = add32(d, od);
  }

  const toHex = (num: number) => {
    let s = "";
    for (let i = 0; i < 4; i++) {
      const byte = (num >> (i * 8)) & 0xff;
      s += byte.toString(16).padStart(2, "0");
    }
    return s;
  };
  return toHex(a) + toHex(b) + toHex(c) + toHex(d);
}

export function gravatarUrl(email: string, size = 72): string {
  const hash = md5((email || "").trim().toLowerCase());
  return `https://www.gravatar.com/avatar/${hash}?d=retro&s=${size}`;
}

export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // fall through to exec fallback
  }
  try {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand("copy");
    document.body.removeChild(ta);
    return ok;
  } catch {
    return false;
  }
}

export const INITIAL_LIMIT = 100;
export const LOAD_STEPS = [100, 1000];
export const ALL_LIMIT = 100000;

export type Scope = "all" | "current";
const SCOPE_KEY = "shelley_git_graph_scope";
export function loadScope(): Scope {
  try {
    const v = localStorage.getItem(SCOPE_KEY);
    if (v === "current" || v === "all") return v;
  } catch {
    // ignore (private mode, etc.)
  }
  return "all";
}
export function storeScope(s: Scope) {
  try {
    localStorage.setItem(SCOPE_KEY, s);
  } catch {
    // ignore
  }
}

// Persisted desktop width (in px) of the commit-detail pane.
const DETAIL_WIDTH_KEY = "shelley_git_graph_detail_width";
export const DETAIL_MIN_PX = 220;
export const DETAIL_DEFAULT_PX = 352; // 22rem at default 16px root
export function loadDetailWidth(): number {
  try {
    const v = localStorage.getItem(DETAIL_WIDTH_KEY);
    if (v) {
      const n = parseInt(v, 10);
      if (Number.isFinite(n) && n >= DETAIL_MIN_PX) return n;
    }
  } catch {
    // ignore
  }
  return DETAIL_DEFAULT_PX;
}
export function storeDetailWidth(px: number) {
  try {
    localStorage.setItem(DETAIL_WIDTH_KEY, String(Math.round(px)));
  } catch {
    // ignore
  }
}
