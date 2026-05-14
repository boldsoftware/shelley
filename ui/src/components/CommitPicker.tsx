import React, { useEffect, useMemo, useRef, useState } from "react";
import { GitDiffInfo } from "../types";

interface CommitPickerProps {
  diffs: GitDiffInfo[];
  selectedDiff: string | null;
  // Right-hand bound. The diff is either "this commit only" or
  // "through working tree"; arbitrary endpoints are no longer supported.
  selectedTo: "working" | "self";
  onChange: (selectedDiff: string, selectedTo: "working" | "self") => void;
  isMobile: boolean;
}

function truncate(s: string, n: number): string {
  if (s.length <= n) return s;
  return s.slice(0, Math.max(0, n - 1)) + "\u2026";
}

function shortHash(id: string): string {
  if (id === "working") return "";
  return id.slice(0, 8);
}

function commitLabel(diffs: GitDiffInfo[], id: string, maxLen = 40): string {
  const d = diffs.find((x) => x.id === id);
  if (!d) return shortHash(id);
  return truncate(d.message, maxLen);
}

// rangeSyntax describes the active selection in compact prose. Used on
// the closed trigger and at the top of the open picker.
function rangeSyntax(
  diffs: GitDiffInfo[],
  selectedDiff: string | null,
  selectedTo: "working" | "self",
): string {
  if (!selectedDiff) return "Choose\u2026";
  if (selectedDiff === "working") return "Working Changes";
  const from = commitLabel(diffs, selectedDiff);
  if (selectedTo === "self") return `${from} (Single Commit)`;
  return `${from} \u2192 Now`;
}

// RangeToggle renders the "Single Commit" vs "Selected Commit → Now"
// segmented control. Used inside the commit picker popover and in the
// diff viewer sidebar so the choice is reachable in both layouts.
export function RangeToggle({
  selectedDiff,
  selectedTo,
  onChange,
}: {
  selectedDiff: string | null;
  selectedTo: "working" | "self";
  onChange: (selectedDiff: string, selectedTo: "working" | "self") => void;
}) {
  const disabled = selectedDiff === null || selectedDiff === "working";
  return (
    <div className="commit-picker-range-toggle" role="radiogroup" aria-label="Diff range">
      <button
        type="button"
        className={`commit-picker-range-btn${selectedTo === "self" ? " active" : ""}`}
        onClick={() => {
          if (selectedDiff && selectedDiff !== "working") onChange(selectedDiff, "self");
        }}
        disabled={disabled}
        role="radio"
        aria-checked={selectedTo === "self"}
        title="Single Commit"
      >
        Single Commit
      </button>
      <button
        type="button"
        className={`commit-picker-range-btn${selectedTo === "working" ? " active" : ""}`}
        onClick={() => {
          if (selectedDiff && selectedDiff !== "working") onChange(selectedDiff, "working");
        }}
        disabled={disabled}
        role="radio"
        aria-checked={selectedTo === "working"}
        title="From the selected commit through the working tree"
      >
        Selected Commit → Now
      </button>
    </div>
  );
}

// CommitPicker is a single-select popover over the commit history.
// Clicking a row selects that commit; the "only this / through working"
// distinction lives on the surrounding range toggle (rendered both in
// the diff viewer header and inside this popover).
function CommitPicker({ diffs, selectedDiff, selectedTo, onChange, isMobile }: CommitPickerProps) {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const popoverRef = useRef<HTMLDivElement>(null);

  const commitDiffs = useMemo(() => diffs.filter((d) => d.id !== "working"), [diffs]);
  const workingDiff = useMemo(() => diffs.find((d) => d.id === "working"), [diffs]);

  const indexOf = (id: string) => commitDiffs.findIndex((d) => d.id === id);

  // Close on outside click and Escape (capture phase + stopPropagation so
  // Escape closes only the picker, not the surrounding diff modal).
  useEffect(() => {
    if (!open) return;
    const onDocDown = (e: MouseEvent) => {
      const t = e.target as Node;
      if (popoverRef.current?.contains(t)) return;
      if (triggerRef.current?.contains(t)) return;
      setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopImmediatePropagation();
        e.preventDefault();
        setOpen(false);
      }
    };
    document.addEventListener("mousedown", onDocDown);
    document.addEventListener("keydown", onKey, true);
    return () => {
      document.removeEventListener("mousedown", onDocDown);
      document.removeEventListener("keydown", onKey, true);
    };
  }, [open]);

  // Focus management: focus the highlighted row on open, return focus to
  // the trigger on close.
  const wasOpenRef = useRef(false);
  useEffect(() => {
    if (open) {
      requestAnimationFrame(() => {
        const root = popoverRef.current;
        if (!root) return;
        const selected = root.querySelector<HTMLElement>(
          ".commit-picker-row-from .commit-picker-row-main",
        );
        const first = root.querySelector<HTMLElement>(".commit-picker-row-main");
        (selected || first)?.focus();
      });
    } else if (wasOpenRef.current) {
      triggerRef.current?.focus();
    }
    wasOpenRef.current = open;
  }, [open]);

  const onListKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp" && e.key !== "Home" && e.key !== "End") {
      return;
    }
    const root = popoverRef.current;
    if (!root) return;
    const rows = Array.from(root.querySelectorAll<HTMLElement>(".commit-picker-row-main"));
    if (rows.length === 0) return;
    const active = document.activeElement as HTMLElement | null;
    const idx = active ? rows.indexOf(active) : -1;
    if (idx < 0 && (e.key === "ArrowDown" || e.key === "ArrowUp")) return;
    let next = idx;
    if (e.key === "ArrowDown") next = Math.min(idx + 1, rows.length - 1);
    else if (e.key === "ArrowUp") next = Math.max(idx - 1, 0);
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = rows.length - 1;
    if (next !== idx) {
      e.preventDefault();
      rows[next]?.focus();
    }
  };

  const pickCommit = (id: string) => {
    onChange(id, selectedTo);
    setOpen(false);
  };
  const pickWorking = () => {
    onChange("working", "working");
    setOpen(false);
  };

  // Render decoration ref chips. Hide merge-base if any remote-style ref
  // (contains "/") is already showing on the same commit, since that
  // upstream ref already conveys the merge-base location.
  const renderRefs = (d: GitDiffInfo) => {
    const refs = d.refs ?? [];
    const hasRemote = refs.some((r) => r.includes("/"));
    const showMergeBase = !!d.isMergeBase && !hasRemote;
    const chips: React.ReactNode[] = refs.map((ref) => {
      const isHead = ref === "HEAD";
      const isRemote = ref.includes("/");
      const cls = [
        "commit-picker-ref",
        isHead && "commit-picker-ref-head",
        isRemote && "commit-picker-ref-remote",
      ]
        .filter(Boolean)
        .join(" ");
      return (
        <span key={ref} className={cls}>
          {ref}
        </span>
      );
    });
    if (showMergeBase) {
      chips.push(
        <span
          key="__mergebase"
          className="commit-picker-ref commit-picker-ref-mergebase"
          title="Merge-base with @{upstream}"
        >
          merge-base
        </span>,
      );
    }
    if (chips.length === 0) return null;
    return <span className="commit-picker-refs">{chips}</span>;
  };

  // Compute which commit rows are inside the active range. In
  // "through working" mode the range covers the working row down to
  // (and including) the selected commit. In "only this" mode just the
  // selected commit lights up.
  const fromIdx = selectedDiff && selectedDiff !== "working" ? indexOf(selectedDiff) : -1;
  const rowInRange = (idx: number) => {
    if (selectedDiff === "working") return false;
    if (fromIdx < 0) return false;
    if (selectedTo === "self") return idx === fromIdx;
    return idx <= fromIdx;
  };
  const workingInRange =
    selectedDiff === "working" || (selectedDiff !== null && selectedTo === "working");

  const renderCommitRow = (d: GitDiffInfo, idx: number) => {
    const isFrom = d.id === selectedDiff;
    const inRange = !isFrom && rowInRange(idx);
    const stats = `+${d.additions}/-${d.deletions}`;
    const hash = shortHash(d.id);

    const classes = [
      "commit-picker-row",
      isFrom && "commit-picker-row-from",
      inRange && "commit-picker-row-in-range",
    ]
      .filter(Boolean)
      .join(" ");

    return (
      <div key={d.id} className={classes}>
        <button type="button" className="commit-picker-row-main" onClick={() => pickCommit(d.id)}>
          <div className="commit-picker-row-marker" aria-hidden="true">
            {isFrom ? "\u25cf" : inRange ? "\u2502" : ""}
          </div>
          <div className="commit-picker-row-text">
            <div className="commit-picker-row-subject">
              {renderRefs(d)}
              <span className="commit-picker-row-message">{d.message}</span>
            </div>
            <div className="commit-picker-row-meta">
              <span className="commit-picker-row-hash">{hash}</span>
              <span className="commit-picker-row-author">{d.author}</span>
              <span className="commit-picker-row-stats">
                {d.filesCount} files {"\u00b7"} {stats}
              </span>
            </div>
          </div>
        </button>
      </div>
    );
  };

  // Range-mode toggle inside the popover, mirroring the one in the diff
  // viewer sidebar/header so users can flip the variant without closing
  // the picker.
  const rangeToggle = (
    <RangeToggle selectedDiff={selectedDiff} selectedTo={selectedTo} onChange={onChange} />
  );

  const list = (
    <div className="commit-picker-list" onKeyDown={onListKeyDown}>
      {workingDiff && (
        <div
          className={
            "commit-picker-row commit-picker-row-working" +
            (selectedDiff === "working" ? " commit-picker-row-from" : "") +
            (workingInRange && selectedDiff !== "working" ? " commit-picker-row-in-range" : "")
          }
        >
          <button type="button" className="commit-picker-row-main" onClick={pickWorking}>
            <div className="commit-picker-row-marker" aria-hidden="true">
              {selectedDiff === "working" ? "\u25cf" : workingInRange ? "\u2502" : ""}
            </div>
            <div className="commit-picker-row-text">
              <div className="commit-picker-row-subject">Working Changes</div>
              <div className="commit-picker-row-meta">
                <span className="commit-picker-row-stats">
                  {workingDiff.filesCount} files {"\u00b7"} +{workingDiff.additions}/-
                  {workingDiff.deletions}
                </span>
              </div>
            </div>
          </button>
        </div>
      )}
      {commitDiffs.map(renderCommitRow)}
      {commitDiffs.length === 0 && !workingDiff && (
        <div className="commit-picker-empty">No commits or working changes.</div>
      )}
    </div>
  );

  const triggerPrimary = rangeSyntax(diffs, selectedDiff, selectedTo);

  const statusLine = (
    <div className="commit-picker-status">
      <span>
        Showing <code>{rangeSyntax(diffs, selectedDiff, selectedTo)}</code>
      </span>
      {rangeToggle}
    </div>
  );

  return (
    <div className="commit-picker">
      <button
        ref={triggerRef}
        type="button"
        className="commit-picker-trigger"
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-label="Commit"
      >
        <div className="commit-picker-trigger-text">
          <div className="commit-picker-trigger-primary">
            <code>{triggerPrimary}</code>
          </div>
        </div>
        <span className="commit-picker-trigger-chevron" aria-hidden="true">
          {"\u25be"}
        </span>
      </button>

      {open && isMobile && (
        <div className="commit-picker-modal-backdrop" onClick={() => setOpen(false)}>
          <div
            ref={popoverRef}
            className="commit-picker-modal"
            onClick={(e) => e.stopPropagation()}
            role="dialog"
            aria-label="Choose commit"
          >
            <div className="commit-picker-modal-header">
              <span>Choose commit</span>
              <button
                type="button"
                className="commit-picker-modal-close"
                onClick={() => setOpen(false)}
                aria-label="Close"
              >
                {"\u00d7"}
              </button>
            </div>
            {statusLine}
            {list}
          </div>
        </div>
      )}

      {open && !isMobile && (
        <div
          ref={popoverRef}
          className="commit-picker-popover"
          role="dialog"
          aria-label="Choose commit"
        >
          {statusLine}
          {list}
        </div>
      )}
    </div>
  );
}

export default CommitPicker;
