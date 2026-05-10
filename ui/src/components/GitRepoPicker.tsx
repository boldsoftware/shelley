import React, { useEffect, useMemo, useRef, useState } from "react";
import { useEscapeClose } from "./useEscapeClose";

// Picker that lists every git repo under (by default) $HOME and lets the
// user fuzzy-filter the list. Designed for the git-graph viewer where we
// already know the user wants a git working tree.

interface GitRepoInfo {
  path: string;
  branch?: string;
  worktree?: boolean;
  // Unix seconds of the most recent .git activity (HEAD/index/FETCH_HEAD).
  last_activity?: number;
}

function formatRelative(ts: number): string {
  const diff = Date.now() / 1000 - ts;
  if (diff < 60) return "just now";
  if (diff < 3600) return `${Math.floor(diff / 60)}m`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h`;
  if (diff < 86400 * 30) return `${Math.floor(diff / 86400)}d`;
  if (diff < 86400 * 365) return `${Math.floor(diff / (86400 * 30))}mo`;
  return `${Math.floor(diff / (86400 * 365))}y`;
}

interface GitReposResponse {
  repos: GitRepoInfo[];
  roots: string[];
  truncated?: boolean;
  elapsed_ms: number;
}

interface GitRepoPickerProps {
  isOpen: boolean;
  currentPath?: string;
  onClose: () => void;
  onSelect: (path: string) => void;
}

// Subsequence fuzzy match. Returns a score (lower is better) and the
// match positions inside `text`, or null when there's no match.
//
// Tiers (from best to worst), each ~10× the next:
//   0  basename starts with the query (case-insensitive)
//   1  basename contains the query as a contiguous substring
//   2  query is a contiguous substring anywhere in the path
//   3  basename matches as subsequence
//   4  full path matches as subsequence
// Within a tier, fewer gaps + earlier match wins. Shorter paths break ties.
function fuzzyScore(text: string, query: string): { score: number; positions: number[] } | null {
  if (!query) return { score: 0, positions: [] };
  const t = text.toLowerCase();
  const q = query.toLowerCase();
  const lastSlash = text.lastIndexOf("/");
  const baseStart = lastSlash + 1;
  const base = t.slice(baseStart);

  const range = (start: number, len: number) => {
    const out: number[] = [];
    for (let i = 0; i < len; i++) out.push(start + i);
    return out;
  };

  // Tier 0: basename prefix.
  if (base.startsWith(q)) {
    return { score: 0 + text.length, positions: range(baseStart, q.length) };
  }
  // Tier 1: contiguous substring in basename.
  {
    const i = base.indexOf(q);
    if (i >= 0) {
      return { score: 10000 + i + text.length, positions: range(baseStart + i, q.length) };
    }
  }
  // Tier 2: contiguous substring in full path.
  {
    const i = t.indexOf(q);
    if (i >= 0) {
      return { score: 20000 + i + text.length, positions: range(i, q.length) };
    }
  }
  // Subsequence (tiers 3 & 4): walk through the text once.
  const positions: number[] = [];
  let ti = 0;
  let lastMatch = -1;
  let gaps = 0;
  for (let qi = 0; qi < q.length; qi++) {
    const c = q[qi];
    let found = -1;
    while (ti < t.length) {
      if (t[ti] === c) {
        found = ti;
        ti++;
        break;
      }
      ti++;
    }
    if (found === -1) return null;
    positions.push(found);
    if (lastMatch >= 0) gaps += found - lastMatch - 1;
    lastMatch = found;
  }
  const baseMatches = positions.filter((p) => p >= baseStart).length;
  const tier = baseMatches === q.length ? 30000 : 40000;
  return { score: tier + gaps * 10 + text.length, positions };
}

// Display the path with $HOME collapsed to ~. Filtering still runs on the
// real path so the user can search by absolute path or by anything inside
// their home dir without having to know about the abbreviation.
function displayPath(p: string, home: string): string {
  if (!home) return p;
  if (p === home) return "~";
  if (p.startsWith(home + "/")) return "~" + p.slice(home.length);
  return p;
}

// Shift highlight positions from real-path coordinates into displayPath
// coordinates. Positions inside the elided $HOME prefix are dropped (they
// can't be shown), and the rest are translated by the length difference.
function shiftPositions(positions: number[], realPath: string, home: string): number[] {
  if (!home || !realPath.startsWith(home + "/")) return positions;
  const cut = home.length - 1; // "~" replaces all of home
  const out: number[] = [];
  for (const pos of positions) {
    if (pos < home.length) continue; // hidden inside ~
    out.push(pos - cut);
  }
  return out;
}

function Highlight({ text, positions }: { text: string; positions: number[] }) {
  if (positions.length === 0) return <>{text}</>;
  const out: React.ReactNode[] = [];
  let cursor = 0;
  let i = 0;
  while (i < positions.length) {
    let j = i;
    while (j + 1 < positions.length && positions[j + 1] === positions[j] + 1) j++;
    const start = positions[i];
    const end = positions[j] + 1;
    if (start > cursor) out.push(text.slice(cursor, start));
    out.push(
      <mark key={start} className="grp-hit">
        {text.slice(start, end)}
      </mark>,
    );
    cursor = end;
    i = j + 1;
  }
  if (cursor < text.length) out.push(text.slice(cursor));
  return <>{out}</>;
}

export default function GitRepoPicker({
  isOpen,
  currentPath,
  onClose,
  onSelect,
}: GitRepoPickerProps) {
  const [repos, setRepos] = useState<GitRepoInfo[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [truncated, setTruncated] = useState(false);
  const [home, setHome] = useState<string>("");
  const [query, setQuery] = useState("");
  const [activeIdx, setActiveIdx] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  useEscapeClose(isOpen, onClose);

  useEffect(() => {
    if (!isOpen) return;
    let cancelled = false;
    setLoading(true);
    setError(null);
    setRepos(null);
    fetch("/api/git/repos")
      .then(async (r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        return (await r.json()) as GitReposResponse;
      })
      .then((d) => {
        if (cancelled) return;
        setRepos(d.repos || []);
        setTruncated(!!d.truncated);
        // The server returns the roots it scanned; first one is $HOME by default.
        const r = (d.roots || [])[0];
        if (r) setHome(r);
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
  }, [isOpen]);

  useEffect(() => {
    if (!isOpen) return;
    setQuery("");
    setActiveIdx(0);
    // Don't auto-focus the input on mobile; it pops the keyboard.
    const isMobile = window.matchMedia("(max-width: 768px)").matches || "ontouchstart" in window;
    if (!isMobile) {
      // Defer to next tick so the dialog is mounted before focusing.
      const id = window.setTimeout(() => inputRef.current?.focus(), 0);
      return () => window.clearTimeout(id);
    }
  }, [isOpen]);

  const filtered = useMemo(() => {
    if (!repos) return [];
    if (!query) {
      // Default sort: current path first, then most recently active first,
      // alphabetical for any ties or repos with no activity stamp.
      const list = [...repos].sort((a, b) => {
        if (currentPath) {
          if (a.path === currentPath) return -1;
          if (b.path === currentPath) return 1;
        }
        const ta = a.last_activity ?? 0;
        const tb = b.last_activity ?? 0;
        if (ta !== tb) return tb - ta;
        return a.path.localeCompare(b.path);
      });
      return list.map((r) => ({ repo: r, positions: [] as number[] }));
    }
    const scored: { repo: GitRepoInfo; score: number; positions: number[] }[] = [];
    for (const r of repos) {
      const m = fuzzyScore(r.path, query);
      if (m) scored.push({ repo: r, score: m.score, positions: m.positions });
    }
    scored.sort((a, b) => a.score - b.score || a.repo.path.localeCompare(b.repo.path));
    return scored.map(({ repo, positions }) => ({ repo, positions }));
  }, [repos, query, currentPath]);

  useEffect(() => {
    setActiveIdx(0);
  }, [query]);

  // Scroll the active row into view.
  useEffect(() => {
    if (!listRef.current) return;
    const row = listRef.current.querySelector<HTMLElement>(`[data-idx="${activeIdx}"]`);
    if (row) row.scrollIntoView({ block: "nearest" });
  }, [activeIdx]);

  const handleKey = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.nativeEvent.isComposing) return;
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setActiveIdx((i) => Math.min(filtered.length - 1, i + 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActiveIdx((i) => Math.max(0, i - 1));
    } else if (e.key === "Enter") {
      e.preventDefault();
      const hit = filtered[activeIdx];
      if (hit) {
        onSelect(hit.repo.path);
        onClose();
      }
    }
  };

  if (!isOpen) return null;

  return (
    <div
      className="modal-overlay"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div className="modal grp-modal" role="dialog" aria-label="Pick a git repository">
        <div className="modal-header">
          <h2 className="modal-title">Pick git repository</h2>
          <button onClick={onClose} className="btn-icon" aria-label="Close">
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M6 18L18 6M6 6l12 12"
              />
            </svg>
          </button>
        </div>
        <div className="modal-body grp-body">
          <input
            ref={inputRef}
            className="grp-filter"
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={handleKey}
            placeholder={
              loading ? "Scanning…" : repos ? `Filter ${repos.length} repos…` : "Filter repos…"
            }
            spellCheck={false}
            aria-label="Filter"
          />

          {error && <div className="grp-error">{error}</div>}

          <div className="grp-list" ref={listRef}>
            {filtered.map((hit, idx) => {
              const r = hit.repo;
              const isActive = idx === activeIdx;
              const isCurrent = r.path === currentPath;
              return (
                <button
                  key={r.path}
                  data-idx={idx}
                  type="button"
                  className={`grp-row${isActive ? " grp-row-active" : ""}${
                    isCurrent ? " grp-row-current" : ""
                  }`}
                  onMouseEnter={() => setActiveIdx(idx)}
                  onClick={() => {
                    onSelect(r.path);
                    onClose();
                  }}
                >
                  <svg
                    className="grp-icon"
                    fill="none"
                    stroke="currentColor"
                    viewBox="0 0 24 24"
                    aria-hidden="true"
                  >
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeWidth={2}
                      d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"
                    />
                  </svg>
                  <span className="grp-main">
                    <span className="grp-path" title={r.path}>
                      <Highlight
                        text={displayPath(r.path, home)}
                        positions={shiftPositions(hit.positions, r.path, home)}
                      />
                    </span>
                    {(r.branch || r.last_activity) && (
                      <span className="grp-meta">
                        {r.branch && (
                          <span className="grp-branch" title="Current branch">
                            {r.branch}
                          </span>
                        )}
                        {r.last_activity && (
                          <span
                            className="grp-when"
                            title={new Date(r.last_activity * 1000).toLocaleString()}
                          >
                            {formatRelative(r.last_activity)}
                          </span>
                        )}
                      </span>
                    )}
                  </span>
                </button>
              );
            })}
            {!loading && repos && filtered.length === 0 && (
              <div className="grp-empty">No matching repos.</div>
            )}
            {loading && <div className="grp-empty">Scanning your home directory…</div>}
          </div>

          {truncated && <div className="grp-truncated">Showing first results — try filtering.</div>}
        </div>
      </div>
    </div>
  );
}
