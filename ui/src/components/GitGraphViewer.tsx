import React, { useEffect, useMemo, useRef, useState, useCallback } from "react";
import { FolderIcon, XIcon } from "lucide-react";
import { api } from "../services/api";
import type { GitGraphCommit, GitGraphResponse, GitCommitDetail } from "../types";
import GitRepoPicker from "./GitRepoPicker";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

interface GitGraphViewerProps {
  cwd: string;
  isOpen: boolean;
  onClose: () => void;
  onOpenDiff?: (commit: string, cwd: string) => void;
  // True when a modal (e.g. DiffViewer) is stacked on top of this one.
  // Suppresses Esc handling so the top-most modal closes first.
  covered?: boolean;
}

// Lane colors cycle by lane index (stable per lane, GitX-style).
const LANE_COLORS = [
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

function laneColor(i: number): string {
  return LANE_COLORS[((i % LANE_COLORS.length) + LANE_COLORS.length) % LANE_COLORS.length];
}

function normalizeCommits(commits: GitGraphCommit[]): GitGraphCommit[] {
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
interface GraphLine {
  upper: boolean;
  from: number;
  to: number;
  colorIndex: number;
}

interface RowInfo {
  col: number; // 1-based column of the commit dot
  colorIndex: number; // lane color for the dot itself
  lines: GraphLine[];
  numColumns: number; // number of active lanes after laying out this row (used for width)
}

// Ported from GitX's PBGitGrapher. Compacts lanes as they die, so columns
// reflow left to fill gaps; colors are assigned per-lane (stable across rows).
function computeLayout(commits: GitGraphCommit[]): { rows: RowInfo[]; maxColumns: number } {
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

const ROW_H = 24;
const LANE_W = 14;
const LEFT_PAD = 10;
const DOT_R = 4;

function colX(col: number): number {
  // col is 1-based.
  return LEFT_PAD + (col - 1) * LANE_W;
}

function formatRelative(ts: number): string {
  const diff = Date.now() / 1000 - ts;
  if (diff < 60) return "just now";
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  if (diff < 86400 * 30) return `${Math.floor(diff / 86400)}d ago`;
  const d = new Date(ts * 1000);
  return d.toLocaleDateString();
}

const REF_BASE =
  "inline-block rounded-sm border border-transparent px-1.5 text-[0.68rem] leading-[1.4]";

function RefBadge({ name }: { name: string }) {
  let tone =
    "bg-emerald-100 text-emerald-900 dark:bg-emerald-500/20 dark:text-emerald-200"; // local
  let label = name;
  if (name === "HEAD") {
    tone = "bg-amber-100 text-amber-900 dark:bg-amber-500/20 dark:text-amber-200"; // head
  } else if (name.startsWith("tag: ")) {
    tone =
      "bg-violet-100 text-violet-900 dark:bg-violet-500/20 dark:text-violet-200"; // tag
    label = name.slice(5);
  } else if (name.startsWith("origin/") || name.includes("/")) {
    tone = "bg-blue-100 text-blue-900 dark:bg-blue-500/20 dark:text-blue-200"; // remote
  }
  return <span className={cn(REF_BASE, tone)}>{label}</span>;
}

// MD5 for Gravatar URLs. Gravatar hashes are MD5 of the trimmed,
// lowercased email — there's no native crypto API for MD5 in browsers,
// so we carry a tiny implementation. Returns a 32-char lowercase hex string.
function md5(input: string): string {
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

  const toHex = (n: number) => {
    let s = "";
    for (let i = 0; i < 4; i++) {
      const byte = (n >> (i * 8)) & 0xff;
      s += byte.toString(16).padStart(2, "0");
    }
    return s;
  };
  return toHex(a) + toHex(b) + toHex(c) + toHex(d);
}

function gravatarUrl(email: string, size = 72): string {
  const hash = md5((email || "").trim().toLowerCase());
  return `https://www.gravatar.com/avatar/${hash}?d=retro&s=${size}`;
}

async function copyText(text: string): Promise<boolean> {
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

function CopyButton({ value, label, title }: { value: string; label: string; title?: string }) {
  const [copied, setCopied] = useState(false);
  const timerRef = useRef<number | null>(null);
  useEffect(
    () => () => {
      if (timerRef.current) window.clearTimeout(timerRef.current);
    },
    [],
  );
  return (
    <button
      type="button"
      className="cursor-pointer rounded-sm border border-border bg-background px-1.5 py-0.5 font-mono text-[0.7rem] leading-[1.4] text-foreground hover:border-muted-foreground/50 hover:bg-muted"
      title={title || `Copy ${label}`}
      onClick={async () => {
        if (await copyText(value)) {
          setCopied(true);
          if (timerRef.current) window.clearTimeout(timerRef.current);
          timerRef.current = window.setTimeout(() => setCopied(false), 1100);
        }
      }}
    >
      {copied ? "copied" : label}
    </button>
  );
}

// Shared diff-add / diff-remove accents (emerald / red) used by the
// diffstat counts, totals and the proportional +/- bar.
const DIFFSTAT_INS = "text-emerald-600 dark:text-emerald-400";
const DIFFSTAT_DEL = "text-red-600 dark:text-red-400";

// Compact git diff --stat style list. Each row: +/- counts, a tiny
// green/red bar scaled by the biggest row, and the path (shortened
// from the left so the filename stays visible).
function DiffstatList({
  files,
}: {
  files: { path: string; additions: number; deletions: number; binary: boolean }[];
}) {
  const maxTotal = Math.max(1, ...files.map((f) => (f.binary ? 0 : f.additions + f.deletions)));
  // 40 chars max wide for the bar; cap smaller for short paths.
  const BAR_CHARS = 24;
  return (
    <ul className="m-0 grid list-none grid-cols-[minmax(0,1fr)_auto_auto] items-baseline gap-x-2 gap-y-0.5 p-0 font-mono">
      {files.map((f) => {
        const total = f.additions + f.deletions;
        const scale = total === 0 ? 0 : Math.max(1, Math.round((total / maxTotal) * BAR_CHARS));
        const adds =
          total === 0
            ? 0
            : Math.max(
                f.additions > 0 ? 1 : 0,
                Math.round((f.additions / Math.max(1, total)) * scale),
              );
        const dels = Math.max(0, scale - adds);
        return (
          <li key={f.path} className="contents" title={f.path}>
            <span className="min-w-0 overflow-hidden text-ellipsis whitespace-nowrap text-foreground">
              {f.path}
            </span>
            <span className="inline-flex justify-self-end gap-1.5 whitespace-nowrap">
              {f.binary ? (
                <span className="italic text-muted-foreground">bin</span>
              ) : (
                <>
                  {f.additions > 0 && (
                    <span className={DIFFSTAT_INS}>+{f.additions}</span>
                  )}
                  {f.deletions > 0 && (
                    <span className={DIFFSTAT_DEL}>−{f.deletions}</span>
                  )}
                </>
              )}
            </span>
            <span className="whitespace-nowrap tracking-[-0.05em]" aria-hidden="true">
              <span className={DIFFSTAT_INS}>{"+".repeat(adds)}</span>
              <span className={DIFFSTAT_DEL}>{"−".repeat(dels)}</span>
            </span>
          </li>
        );
      })}
    </ul>
  );
}

// Octocat mark (simplified) — inline SVG so we don't depend on external fonts.
function OctocatIcon({ size = 16 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
      <path d="M8 0C3.58 0 0 3.58 0 8a8 8 0 0 0 5.47 7.59c.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27s1.36.09 2 .27c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8z" />
    </svg>
  );
}

const INITIAL_LIMIT = 100;
const LOAD_STEPS = [100, 1000];
const ALL_LIMIT = 100000;

type Scope = "all" | "current";
const SCOPE_KEY = "shelley_git_graph_scope";
function loadScope(): Scope {
  try {
    const v = localStorage.getItem(SCOPE_KEY);
    if (v === "current" || v === "all") return v;
  } catch {
    // ignore (private mode, etc.)
  }
  return "all";
}

// Persisted desktop width (in px) of the commit-detail pane. The user can
// drag a divider between the commit list and the detail pane to make the
// commit message / diffstat easier to read. Mobile uses a bottom sheet,
// so this preference is ignored there.
const DETAIL_WIDTH_KEY = "shelley_git_graph_detail_width";
const DETAIL_MIN_PX = 220;
const DETAIL_DEFAULT_PX = 352; // 22rem at default 16px root
function loadDetailWidth(): number {
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

export default function GitGraphViewer({
  cwd: cwdProp,
  isOpen,
  onClose,
  onOpenDiff,
  covered = false,
}: GitGraphViewerProps) {
  // Internal override so the user can switch directories without re-opening.
  const [cwdOverride, setCwdOverride] = useState<string | null>(null);
  const cwd = cwdOverride ?? cwdProp;
  const [showCwdPicker, setShowCwdPicker] = useState(false);
  const [data, setData] = useState<GitGraphResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [limit, setLimit] = useState(INITIAL_LIMIT);
  const [scope, setScopeState] = useState<Scope>(loadScope);
  const setScope = useCallback((s: Scope) => {
    setScopeState(s);
    try {
      localStorage.setItem(SCOPE_KEY, s);
    } catch {
      // ignore
    }
  }, []);
  // Full body + numstat for the selected commit. Lazily loaded when the
  // selection changes, with a small cache so re-selecting is instant.
  const [detail, setDetail] = useState<GitCommitDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const detailCacheRef = useRef<Map<string, GitCommitDetail>>(new Map());
  // On mobile (<=768px) the sidebar is a bottom sheet that only shows
  // after the user taps a row. On desktop the same flag is ignored
  // because the sidebar is always visible.
  const [sheetOpen, setSheetOpen] = useState(false);

  // Desktop-only: width of the commit-detail pane, controlled by the
  // draggable divider. Persisted in localStorage so it survives reloads.
  const [detailWidth, setDetailWidth] = useState<number>(loadDetailWidth);
  // Track desktop vs mobile so we only apply the persisted width on desktop.
  // On mobile the detail pane is a bottom sheet whose width fills the screen.
  const [isDesktop, setIsDesktop] = useState<boolean>(() =>
    typeof window === "undefined" ? true : !window.matchMedia("(max-width: 768px)").matches,
  );
  useEffect(() => {
    const mq = window.matchMedia("(max-width: 768px)");
    const onChange = () => setIsDesktop(!mq.matches);
    if (mq.addEventListener) mq.addEventListener("change", onChange);
    else mq.addListener(onChange);
    return () => {
      if (mq.removeEventListener) mq.removeEventListener("change", onChange);
      else mq.removeListener(onChange);
    };
  }, []);
  const bodyRef = useRef<HTMLDivElement>(null);
  const isDraggingRef = useRef(false);
  const onDividerMouseDown = useCallback((e: React.MouseEvent) => {
    // Only respond to primary button; ignore on touch/mobile bottom-sheet.
    if (e.button !== 0) return;
    if (window.matchMedia("(max-width: 768px)").matches) return;
    e.preventDefault();
    isDraggingRef.current = true;
    document.body.style.cursor = "col-resize";
    document.body.style.userSelect = "none";
    const onMove = (ev: MouseEvent) => {
      if (!isDraggingRef.current) return;
      const body = bodyRef.current;
      if (!body) return;
      const rect = body.getBoundingClientRect();
      // Dragging right shrinks detail; dragging left grows it.
      const next = Math.max(
        DETAIL_MIN_PX,
        Math.min(rect.width - DETAIL_MIN_PX, rect.right - ev.clientX),
      );
      setDetailWidth(next);
    };
    const onUp = () => {
      if (!isDraggingRef.current) return;
      isDraggingRef.current = false;
      document.body.style.cursor = "";
      document.body.style.userSelect = "";
      document.removeEventListener("mousemove", onMove);
      document.removeEventListener("mouseup", onUp);
      try {
        // Read latest via the closure-free state setter trick: persist on next tick.
      } catch {
        // ignore
      }
    };
    document.addEventListener("mousemove", onMove);
    document.addEventListener("mouseup", onUp);
  }, []);
  // Persist whenever width changes (post-drag, or programmatically).
  useEffect(() => {
    try {
      localStorage.setItem(DETAIL_WIDTH_KEY, String(Math.round(detailWidth)));
    } catch {
      // ignore
    }
  }, [detailWidth]);
  // Double-click resets to default — easy escape hatch if a user drags too far.
  const onDividerDoubleClick = useCallback(() => {
    setDetailWidth(DETAIL_DEFAULT_PX);
  }, []);

  useEffect(() => {
    if (!isOpen || !cwd) return;
    let cancelled = false;
    setLoading(true);
    setError(null);
    api
      .getGitGraph(cwd, limit, scope)
      .then((d) => {
        if (cancelled) return;
        d.commits = normalizeCommits(d.commits || []);
        setData(d);
        if (d.commits.length > 0) {
          setSelected((prev) => {
            if (prev && d.commits.some((c) => c.hash === prev)) return prev;
            const head = d.commits.find((c) => c.isHead);
            return (head ?? d.commits[0]).hash;
          });
        }
      })
      .catch((e) => {
        if (!cancelled) setError(String(e?.message || e));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [isOpen, cwd, limit, scope]);

  useEffect(() => {
    if (!isOpen) {
      setSelected(null);
      setLimit(INITIAL_LIMIT);
      setDetail(null);
      setSheetOpen(false);
      setCwdOverride(null);
      setShowCwdPicker(false);
      detailCacheRef.current.clear();
    }
  }, [isOpen]);

  // Reset transient state when cwd changes so we don't show stale selection/detail.
  useEffect(() => {
    setSelected(null);
    setData(null);
    setDetail(null);
    setLimit(INITIAL_LIMIT);
    detailCacheRef.current.clear();
  }, [cwd]);

  // Load commit body + numstat on selection.
  useEffect(() => {
    if (!isOpen || !cwd || !selected) {
      setDetail(null);
      return;
    }
    const cached = detailCacheRef.current.get(selected);
    if (cached) {
      setDetail(cached);
      setDetailLoading(false);
      return;
    }
    let cancelled = false;
    setDetail(null);
    setDetailLoading(true);
    api
      .getGitCommitDetail(cwd, selected)
      .then((d) => {
        if (cancelled) return;
        detailCacheRef.current.set(selected, d);
        setDetail(d);
      })
      .catch(() => {
        // Silently ignore; the sidebar still shows the graph-level info.
      })
      .finally(() => {
        if (!cancelled) setDetailLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [isOpen, cwd, selected]);

  useEffect(() => {
    if (!isOpen || covered) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        // If the mobile bottom sheet is open, close it first instead of
        // tearing down the whole viewer.
        if (sheetOpen && window.matchMedia("(max-width: 768px)").matches) {
          setSheetOpen(false);
          return;
        }
        onClose();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [isOpen, covered, onClose, sheetOpen]);

  const commits = data?.commits ?? [];
  const layout = useMemo(() => computeLayout(commits), [commits]);

  // Per-row width: just wide enough to cover that row's rightmost lane, so
  // commit subjects drift leftward and stay snug with the graph.
  const rowWidth = useCallback(
    (i: number): number => {
      const row = layout.rows[i];
      if (!row) return LEFT_PAD + LANE_W;
      let maxCol = row.col;
      for (const ln of row.lines) {
        if (ln.from > maxCol) maxCol = ln.from;
        if (ln.to > maxCol) maxCol = ln.to;
      }
      return LEFT_PAD + (maxCol - 1) * LANE_W + LANE_W;
    },
    [layout],
  );

  const selectCommit = useCallback((hash: string) => {
    setSelected(hash);
    setSheetOpen(true);
  }, []);

  useEffect(() => {
    if (!isOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return;
      if (!commits.length) return;
      const idx = commits.findIndex((c) => c.hash === selected);
      if (e.key === "ArrowDown" || e.key === "j") {
        e.preventDefault();
        const next = commits[Math.min(commits.length - 1, Math.max(0, idx + 1))];
        if (next) selectCommit(next.hash);
      } else if (e.key === "ArrowUp" || e.key === "k") {
        e.preventDefault();
        const prev = commits[Math.max(0, idx - 1)];
        if (prev) selectCommit(prev.hash);
      } else if (e.key === "Enter" && selected && onOpenDiff) {
        e.preventDefault();
        onOpenDiff(selected, cwd);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [isOpen, commits, selected, selectCommit, onOpenDiff, cwd]);

  const selectedCommit = commits.find((c) => c.hash === selected) || null;

  const selectedRowRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (selectedRowRef.current) {
      selectedRowRef.current.scrollIntoView({ block: "nearest" });
    }
  }, [selected]);

  if (!isOpen) return null;

  return (
    <div
      className="fixed inset-0 z-[1000] flex items-center justify-center bg-black/50 p-4"
      onClick={onClose}
    >
      <div
        className="relative flex h-[calc(100%-2rem)] w-[calc(100%-2rem)] flex-col overflow-hidden rounded-lg bg-background text-[0.82rem] text-foreground shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="absolute top-2 right-2 z-[5] inline-flex items-center gap-1 rounded-full border border-border bg-background p-0.5 shadow-sm">
          <div
            className="inline-flex items-center rounded-full bg-muted p-0.5"
            role="group"
            aria-label="Branch scope"
            title="Which refs to walk"
          >
            <button
              type="button"
              className={cn(
                "cursor-pointer rounded-full px-2 py-0.5 text-[0.72rem] leading-tight whitespace-nowrap",
                scope === "all"
                  ? "bg-background text-foreground shadow-sm"
                  : "text-muted-foreground hover:text-foreground",
              )}
              onClick={() => setScope("all")}
              aria-pressed={scope === "all"}
              title="Show commits from all branches"
            >
              All branches
            </button>
            <button
              type="button"
              className={cn(
                "cursor-pointer rounded-full px-2 py-0.5 text-[0.72rem] leading-tight whitespace-nowrap",
                scope === "current"
                  ? "bg-background text-foreground shadow-sm"
                  : "text-muted-foreground hover:text-foreground",
              )}
              onClick={() => setScope("current")}
              aria-pressed={scope === "current"}
              title="Show commits reachable from HEAD only"
            >
              Current branch
            </button>
          </div>
          <Button
            variant="ghost"
            size="icon-sm"
            className="rounded-full text-muted-foreground hover:text-foreground"
            onClick={() => setShowCwdPicker(true)}
            title={`Pick repository (current: ${cwd})`}
            aria-label="Pick repository"
          >
            <FolderIcon className="size-4" aria-hidden="true" />
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            className="rounded-full text-muted-foreground hover:text-foreground"
            onClick={onClose}
            title="Close (Esc)"
            aria-label="Close"
          >
            <XIcon className="size-4" aria-hidden="true" />
          </Button>
        </div>
        <GitRepoPicker
          isOpen={showCwdPicker}
          currentPath={cwd}
          onClose={() => setShowCwdPicker(false)}
          onSelect={(p) => setCwdOverride(p)}
        />

        <div className="relative flex min-h-0 flex-1 overflow-hidden" ref={bodyRef}>
          <div className="min-w-0 flex-1 overflow-auto bg-background pt-1 pb-2 font-mono">
            {loading && !data && (
              <div className="p-4 text-muted-foreground">Loading…</div>
            )}
            {error && <div className="p-4 text-destructive">{error}</div>}
            {!loading && !error && commits.length === 0 && (
              <div className="p-4 text-muted-foreground">No commits.</div>
            )}
            {commits.length > 0 && (
              <>
                {commits.map((c, i) => {
                  const row = layout.rows[i];
                  const isSel = c.hash === selected;
                  return (
                    <div
                      key={c.hash}
                      ref={isSel ? selectedRowRef : null}
                      className={cn(
                        "flex cursor-pointer items-center gap-2 overflow-hidden px-2 text-[0.8rem] whitespace-nowrap",
                        isSel ? "bg-primary/15" : "hover:bg-muted",
                      )}
                      style={{ height: ROW_H }}
                      onClick={() => selectCommit(c.hash)}
                      onDoubleClick={() => onOpenDiff && onOpenDiff(c.hash, cwd)}
                    >
                      <span className="hidden w-[7ch] shrink-0 text-[0.78rem] text-muted-foreground md:inline">
                        {c.shortHash}
                      </span>
                      <svg
                        className="git-graph-svg block shrink-0 pointer-events-none"
                        width={rowWidth(i)}
                        height={ROW_H}
                        style={{ width: rowWidth(i), height: ROW_H }}
                      >
                        {row.lines.map((ln, idx) => {
                          const x1 = colX(ln.from);
                          const x2 = colX(ln.to);
                          // GitX convention: `from` is the column at the cell
                          // edge (top for upper, bottom for lower), `to` is the
                          // column at the cell's vertical midpoint (the dot row).
                          // Drawing lower as (from,mid)->(to,bot) flips merge
                          // diagonals and breaks lane alignment between rows.
                          const yTop = 0;
                          const yMid = ROW_H / 2;
                          const yBot = ROW_H;
                          const y1 = ln.upper ? yTop : yBot;
                          const y2 = yMid;
                          return (
                            <line
                              key={idx}
                              x1={x1}
                              y1={y1}
                              x2={x2}
                              y2={y2}
                              stroke={laneColor(ln.colorIndex)}
                              strokeWidth={1.8}
                              strokeLinecap="round"
                            />
                          );
                        })}
                        <circle
                          cx={colX(row.col)}
                          cy={ROW_H / 2}
                          r={DOT_R}
                          fill={laneColor(row.colorIndex)}
                          stroke={c.isHead ? "var(--foreground)" : "none"}
                          strokeWidth={c.isHead ? 1.5 : 0}
                        >
                          <title>{c.shortHash}</title>
                        </circle>
                      </svg>
                      <span className="inline-flex min-w-0 flex-1 items-center gap-1.5 overflow-hidden font-sans text-foreground">
                        {(c.refs.length > 0 ||
                          (c.isMergeBase && !c.refs.some((r) => r.includes("/")))) && (
                          <span className="inline-flex shrink-0 gap-0.5">
                            {c.refs.map((r) => (
                              <RefBadge key={r} name={r} />
                            ))}
                            {c.isMergeBase && !c.refs.some((r) => r.includes("/")) && (
                              <span
                                className={cn(
                                  REF_BASE,
                                  "border-dashed border-border italic text-muted-foreground",
                                )}
                                title="Merge-base with @{upstream}"
                              >
                                merge-base
                              </span>
                            )}
                          </span>
                        )}
                        <span className="overflow-hidden text-ellipsis whitespace-nowrap">
                          {c.subject}
                        </span>
                      </span>
                      <span className="hidden max-w-[14ch] shrink-0 overflow-hidden text-ellipsis font-sans text-[0.74rem] text-muted-foreground md:block">
                        {c.author}
                      </span>
                      <span className="shrink-0 min-w-[5rem] text-right font-sans text-[0.74rem] text-muted-foreground">
                        {formatRelative(c.timestamp)}
                      </span>
                    </div>
                  );
                })}
                {/* Load-more footer. We show options when the server returned
                    at least as many commits as requested, i.e. there's likely more. */}
                <LoadMoreRow
                  limit={limit}
                  commitsLoaded={commits.length}
                  loading={loading}
                  onLoad={setLimit}
                />
              </>
            )}
          </div>

          {selectedCommit && (
            <>
              {sheetOpen && (
                <div
                  className="absolute inset-0 z-[5] bg-black/35 md:hidden"
                  onClick={() => setSheetOpen(false)}
                  aria-hidden="true"
                />
              )}
              {/* Draggable divider — desktop only; hidden on mobile
                  because the detail pane becomes a bottom sheet there. */}
              <div
                className="group relative hidden w-1.5 shrink-0 cursor-col-resize touch-none items-center justify-center select-none hover:bg-muted md:flex"
                role="separator"
                aria-orientation="vertical"
                aria-label="Resize commit details (double-click to reset)"
                title="Drag to resize — double-click to reset"
                onMouseDown={onDividerMouseDown}
                onDoubleClick={onDividerDoubleClick}
              >
                <div
                  className="h-8 w-0.5 rounded-full bg-muted-foreground opacity-50 group-hover:opacity-90"
                  aria-hidden="true"
                />
              </div>
              <div
                className={cn(
                  "flex flex-col gap-2 overflow-auto bg-muted/40 px-4 py-3",
                  // Desktop: static side pane with a left border; width comes
                  // from the draggable divider (inline style below).
                  "md:shrink-0 md:border-l md:border-border",
                  // Mobile: a bottom sheet that slides up over a backdrop.
                  "max-md:absolute max-md:inset-x-0 max-md:bottom-0 max-md:z-[6] max-md:h-[75vh] max-md:max-h-[85%] max-md:rounded-t-xl max-md:border-t max-md:border-border max-md:pt-1 max-md:shadow-[0_-8px_24px_rgba(0,0,0,0.25)] max-md:transition-transform max-md:duration-200",
                  sheetOpen ? "max-md:translate-y-0" : "max-md:translate-y-full max-md:pointer-events-none",
                )}
                role="dialog"
                aria-label="Commit details"
                style={isDesktop ? { width: `${detailWidth}px` } : undefined}
              >
                <div className="sticky top-0 z-[2] m-0 hidden h-0 shrink-0 pointer-events-none max-md:block">
                  <button
                    type="button"
                    className="pointer-events-auto absolute top-2 right-2 inline-flex size-7 items-center justify-center rounded-full border border-border bg-background p-0 text-lg leading-none text-foreground"
                    onClick={() => setSheetOpen(false)}
                    aria-label="Close commit details"
                    title="Close details"
                  >
                    ×
                  </button>
                </div>
                <div className="flex items-start gap-2.5 pt-9 pr-9">
                  <img
                    className="size-12 shrink-0 rounded-md bg-background object-cover"
                    src={gravatarUrl(selectedCommit.email, 72)}
                    alt=""
                    width={48}
                    height={48}
                    referrerPolicy="no-referrer"
                    onError={(e) => {
                      (e.currentTarget as HTMLImageElement).style.visibility = "hidden";
                    }}
                  />
                  <div className="font-semibold whitespace-pre-wrap text-foreground">
                    {selectedCommit.subject}
                  </div>
                </div>

                <div className="flex flex-col gap-1 text-[0.78rem] text-muted-foreground">
                  <div>
                    <strong>Author:</strong> {selectedCommit.author}
                    {selectedCommit.email ? ` <${selectedCommit.email}>` : ""}
                  </div>
                  <div>
                    <strong>Date:</strong>{" "}
                    {new Date(selectedCommit.timestamp * 1000).toLocaleString()}
                  </div>
                  <div className="flex flex-wrap items-center gap-1.5">
                    <strong>SHA:</strong>{" "}
                    <code className="text-[0.72rem] break-all text-muted-foreground">
                      {selectedCommit.hash}
                    </code>
                    <span className="ml-auto inline-flex gap-0.5">
                      <CopyButton value={selectedCommit.hash} label="sha" title="Copy full SHA" />
                      <CopyButton
                        value={selectedCommit.shortHash}
                        label="short"
                        title="Copy short SHA"
                      />
                      {data?.githubBase && (
                        <CopyButton
                          value={`${data.githubBase}/commit/${selectedCommit.hash}`}
                          label="url"
                          title="Copy GitHub URL"
                        />
                      )}
                    </span>
                  </div>
                  {(selectedCommit.refs.length > 0 ||
                    (selectedCommit.isMergeBase &&
                      !selectedCommit.refs.some((r) => r.includes("/")))) && (
                    <div className="mt-1 flex flex-wrap gap-1">
                      {selectedCommit.refs.map((r) => (
                        <RefBadge key={r} name={r} />
                      ))}
                      {selectedCommit.isMergeBase &&
                        !selectedCommit.refs.some((r) => r.includes("/")) && (
                          <span
                            className={cn(
                              REF_BASE,
                              "border-dashed border-border italic text-muted-foreground",
                            )}
                            title="Merge-base with @{upstream}"
                          >
                            merge-base
                          </span>
                        )}
                    </div>
                  )}
                  {data?.gitRoot && (
                    <div className="mt-2 text-[0.72rem] break-all">
                      <code>{data.gitRoot}</code>
                      {data.currentBranch && (
                        <>
                          {" "}
                          on <strong>{data.currentBranch}</strong>
                        </>
                      )}
                    </div>
                  )}
                </div>

                {detail && detail.body && (
                  <pre className="m-0 rounded-md border border-border bg-background px-2.5 py-2 font-mono text-[0.75rem] leading-snug whitespace-pre-wrap break-words text-foreground">
                    {detail.body}
                  </pre>
                )}

                {detail && detail.files.length > 0 && (
                  <div className="flex flex-col gap-1 text-[0.72rem]">
                    <div className="text-[0.72rem] text-muted-foreground">
                      {detail.files.length} file{detail.files.length === 1 ? "" : "s"} changed
                      {detail.insTotal > 0 && (
                        <span className={DIFFSTAT_INS}> +{detail.insTotal}</span>
                      )}
                      {detail.delTotal > 0 && (
                        <span className={DIFFSTAT_DEL}> −{detail.delTotal}</span>
                      )}
                    </div>
                    <DiffstatList files={detail.files} />
                  </div>
                )}
                {detailLoading && !detail && (
                  <div className="text-[0.75rem] italic text-muted-foreground">Loading…</div>
                )}

                <div className="mt-1 flex items-center gap-2">
                  <a
                    className={cn(
                      "inline-block rounded-md bg-primary px-2.5 py-1.5 text-[0.78rem] text-primary-foreground no-underline hover:bg-primary/90",
                      !onOpenDiff && "pointer-events-none cursor-not-allowed opacity-50",
                    )}
                    href={(() => {
                      const params = new URLSearchParams();
                      params.set("diff", selectedCommit.hash);
                      if (cwd) params.set("cwd", cwd);
                      return `${window.location.pathname}?${params.toString()}`;
                    })()}
                    aria-disabled={!onOpenDiff}
                    onClick={(e) => {
                      // Let the browser handle modifier/middle-click so users
                      // can open the diff in a new tab/window.
                      if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) {
                        return;
                      }
                      e.preventDefault();
                      if (onOpenDiff) onOpenDiff(selectedCommit.hash, cwd);
                    }}
                  >
                    Open diff →
                  </a>
                  {data?.githubBase && (
                    <a
                      className="inline-flex items-center gap-1.5 rounded-md border border-border bg-background px-2 py-1 text-[0.75rem] text-foreground no-underline hover:border-muted-foreground/50 hover:bg-muted"
                      href={`${data.githubBase}/commit/${selectedCommit.hash}`}
                      target="_blank"
                      rel="noopener noreferrer"
                      title="View on GitHub"
                    >
                      <OctocatIcon size={14} />
                      <span>GitHub</span>
                    </a>
                  )}
                </div>
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function LoadMoreRow({
  limit,
  commitsLoaded,
  loading,
  onLoad,
}: {
  limit: number;
  commitsLoaded: number;
  loading: boolean;
  onLoad: (n: number) => void;
}) {
  // If the server returned fewer commits than we asked for, we've reached the
  // repo's history; no point in showing load-more.
  if (commitsLoaded < limit) {
    return (
      <div className="px-4 py-3 text-center font-sans text-[0.82rem] italic text-muted-foreground opacity-70">
        — end of history ({commitsLoaded} commits) —
      </div>
    );
  }
  return (
    <div className="px-4 py-3 text-center font-sans text-[0.82rem] text-muted-foreground">
      {loading ? (
        <span>Loading…</span>
      ) : (
        <>
          Load{" "}
          {LOAD_STEPS.map((step, i) => (
            <React.Fragment key={step}>
              {i > 0 && " / "}
              <a
                href="#"
                className="text-primary no-underline hover:underline"
                onClick={(e) => {
                  e.preventDefault();
                  onLoad(limit + step);
                }}
              >
                {step} more
              </a>
            </React.Fragment>
          ))}
          {" / "}
          <a
            href="#"
            className="text-primary no-underline hover:underline"
            onClick={(e) => {
              e.preventDefault();
              onLoad(ALL_LIMIT);
            }}
          >
            all
          </a>
        </>
      )}
    </div>
  );
}
