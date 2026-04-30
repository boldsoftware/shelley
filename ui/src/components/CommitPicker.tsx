import React, { useEffect, useMemo, useRef, useState } from "react";
import { GitDiffInfo } from "../types";

interface CommitPickerProps {
  diffs: GitDiffInfo[];
  selectedDiff: string | null;
  // Right-hand bound: "working", "self", or a commit hash.
  selectedTo: string;
  onChange: (selectedDiff: string, selectedTo: string) => void;
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

// rangeSyntax produces a compact git-syntax-flavoured description of the
// currently active selection. Used both on the closed trigger and in the
// open picker's status header so users always see exactly what's shown.
//
// Examples:
//   selectedDiff=null            -> "Choose\u2026"
//   selectedDiff="working"       -> "Working Changes"
//   to="self"                    -> "<hash> (only this commit)"
//   to="working"|""              -> "<hash>^\u2026 (through working tree)"
//   to=<hash2>                   -> "<hash>^\u2026<hash2>"
function rangeSyntax(selectedDiff: string | null, selectedTo: string): string {
  if (!selectedDiff) return "Choose\u2026";
  if (selectedDiff === "working") return "Working Changes";
  const h = shortHash(selectedDiff);
  if (selectedTo === "self") return `${h} (only this commit)`;
  if (selectedTo === "" || selectedTo === "working") {
    return `${h}^\u2026 (through working tree)`;
  }
  return `${h}^\u2026${shortHash(selectedTo)}`;
}

// CommitPicker is a single-control replacement for the prior pair of
// <select> elements. Each commit row exposes three actions:
//   "this^\u2026"   -> from=this, to=working   (this commit through working tree)
//   "this"          -> from=this, to=self      (only this commit)
//   "this^\u2026?"  -> set this as the range anchor; choose another commit
//                       as the other endpoint (button labels on other rows
//                       relabel to reflect what clicking them will produce).
function CommitPicker({ diffs, selectedDiff, selectedTo, onChange, isMobile }: CommitPickerProps) {
  const [open, setOpen] = useState(false);
  // pendingFrom holds the anchor commit while the picker waits for the
  // user to choose the other end of the range. null means no pending
  // range; any row click is interpreted as a one-shot action.
  const [pendingFrom, setPendingFrom] = useState<string | null>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const popoverRef = useRef<HTMLDivElement>(null);

  const commitDiffs = useMemo(() => diffs.filter((d) => d.id !== "working"), [diffs]);
  const workingDiff = useMemo(() => diffs.find((d) => d.id === "working"), [diffs]);

  const indexOf = (id: string) => commitDiffs.findIndex((d) => d.id === id);

  // Reset pendingFrom whenever the picker closes so the next open starts fresh.
  useEffect(() => {
    if (!open) setPendingFrom(null);
  }, [open]);

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
        // Inside pending mode, Escape cancels the pending anchor first;
        // a second Escape closes the picker.
        if (pendingFrom !== null) {
          setPendingFrom(null);
        } else {
          setOpen(false);
        }
      }
    };
    document.addEventListener("mousedown", onDocDown);
    document.addEventListener("keydown", onKey, true);
    return () => {
      document.removeEventListener("mousedown", onDocDown);
      document.removeEventListener("keydown", onKey, true);
    };
  }, [open, pendingFrom]);

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

  // Arrow-key navigation between commit rows.
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

  // Action helpers. All of these close the picker on success (except
  // setAnchor, which opens pending mode).
  const pickThrough = (id: string) => {
    onChange(id, "working");
    setOpen(false);
  };
  const pickOnly = (id: string) => {
    onChange(id, "self");
    setOpen(false);
  };
  const pickWorking = () => {
    onChange("working", "working");
    setOpen(false);
  };
  const setAnchor = (id: string) => {
    setPendingFrom(id);
    // Preview as `id^\u2026` so the diff updates while the user picks the
    // second endpoint. They can still cancel via Escape.
    onChange(id, "working");
  };
  const completeRange = (anchor: string, otherEnd: string) => {
    if (anchor === otherEnd) {
      // Closing on the anchor itself reduces to "this^\u2026".
      pickThrough(anchor);
      return;
    }
    const a = indexOf(anchor);
    const b = indexOf(otherEnd);
    if (a < 0 || b < 0) {
      pickThrough(otherEnd);
      return;
    }
    // commitDiffs is newest-first, so the smaller index is the newer
    // commit. The from end of a git range is older.
    const fromId = a < b ? otherEnd : anchor;
    const toId = a < b ? anchor : otherEnd;
    onChange(fromId, toId);
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

  // Render the three (or contextual) action buttons for a single commit row.
  const renderRowActions = (d: GitDiffInfo) => {
    const isAnchor = pendingFrom === d.id;
    if (pendingFrom !== null && !isAnchor) {
      // Pending mode, on a non-anchor row: a single button that completes
      // the range. Label adjusts to show the resulting normalized range.
      const a = indexOf(pendingFrom);
      const b = indexOf(d.id);
      let label = "";
      if (a >= 0 && b >= 0) {
        // newest-first list: smaller index == newer.
        const fromId = a < b ? d.id : pendingFrom;
        const toId = a < b ? pendingFrom : d.id;
        label = `${shortHash(fromId)}^\u2026${shortHash(toId)}`;
      } else {
        label = `\u2192 here`;
      }
      return (
        <div className="commit-picker-row-actions">
          <button
            type="button"
            className="commit-picker-action commit-picker-action-primary"
            onClick={(e) => {
              e.stopPropagation();
              completeRange(pendingFrom, d.id);
            }}
            title="Complete the range"
          >
            {label}
          </button>
        </div>
      );
    }
    if (isAnchor) {
      // Pending-mode, on the anchor: offer Cancel.
      return (
        <div className="commit-picker-row-actions">
          <button
            type="button"
            className="commit-picker-action"
            onClick={(e) => {
              e.stopPropagation();
              setPendingFrom(null);
            }}
            title="Cancel range selection"
          >
            cancel
          </button>
        </div>
      );
    }
    // Default mode: three buttons.
    return (
      <div className="commit-picker-row-actions">
        <button
          type="button"
          className="commit-picker-action"
          onClick={(e) => {
            e.stopPropagation();
            pickThrough(d.id);
          }}
          title="This commit through the working tree"
        >
          {"this^\u2026"}
        </button>
        <button
          type="button"
          className="commit-picker-action"
          onClick={(e) => {
            e.stopPropagation();
            pickOnly(d.id);
          }}
          title="Only this commit"
        >
          this
        </button>
        <button
          type="button"
          className="commit-picker-action"
          onClick={(e) => {
            e.stopPropagation();
            setAnchor(d.id);
          }}
          title="Pick another commit as the other end of the range"
        >
          {"this^\u2026?"}
        </button>
      </div>
    );
  };

  // Click on a commit row body. In default mode, this is shorthand for
  // "this^\u2026". In pending mode, clicking any non-anchor row body
  // completes the range; clicking the anchor row body is a no-op so the
  // user has to be explicit (cancel button or click another row).
  const onRowClick = (d: GitDiffInfo) => {
    if (pendingFrom === null) {
      pickThrough(d.id);
      return;
    }
    if (d.id === pendingFrom) return;
    completeRange(pendingFrom, d.id);
  };

  // Render a single commit row.
  const renderCommitRow = (d: GitDiffInfo, idx: number) => {
    const effectiveFrom = pendingFrom ?? selectedDiff;
    const effectiveTo = pendingFrom ? "working" : selectedTo;

    const isFrom = d.id === effectiveFrom;
    const isTo =
      effectiveFrom !== null &&
      effectiveFrom !== "working" &&
      effectiveTo !== "" &&
      effectiveTo !== "working" &&
      effectiveTo !== "self" &&
      d.id === effectiveTo;
    const fromIdx = effectiveFrom ? indexOf(effectiveFrom) : -1;
    const toIdx =
      effectiveTo !== "" && effectiveTo !== "working" && effectiveTo !== "self"
        ? indexOf(effectiveTo)
        : -1;
    const inRange =
      !isFrom &&
      !isTo &&
      effectiveFrom !== null &&
      effectiveFrom !== "working" &&
      fromIdx >= 0 &&
      idx < fromIdx &&
      (effectiveTo === "working" || (toIdx >= 0 && idx > toIdx));

    const stats = `+${d.additions}/-${d.deletions}`;
    const hash = shortHash(d.id);

    const classes = [
      "commit-picker-row",
      isFrom && "commit-picker-row-from",
      isTo && "commit-picker-row-to",
      inRange && "commit-picker-row-in-range",
      pendingFrom === d.id && "commit-picker-row-pending",
    ]
      .filter(Boolean)
      .join(" ");

    return (
      <div key={d.id} className={classes}>
        <button
          type="button"
          className="commit-picker-row-main"
          onClick={() => onRowClick(d)}
          title={pendingFrom && pendingFrom !== d.id ? "Complete the range here" : ""}
        >
          <div className="commit-picker-row-marker" aria-hidden="true">
            {isFrom ? "\u25cf" : isTo ? "\u25cb" : inRange ? "\u2502" : ""}
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
        {renderRowActions(d)}
      </div>
    );
  };

  const list = (
    <div className="commit-picker-list" onKeyDown={onListKeyDown}>
      {workingDiff && (
        <div
          className={
            "commit-picker-row commit-picker-row-working" +
            (selectedDiff === "working" && pendingFrom === null ? " commit-picker-row-from" : "")
          }
        >
          <button
            type="button"
            className="commit-picker-row-main"
            onClick={pickWorking}
            disabled={pendingFrom !== null}
          >
            <div className="commit-picker-row-marker" aria-hidden="true">
              {selectedDiff === "working" && pendingFrom === null ? "\u25cf" : ""}
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

  // The trigger button shows the active range in git syntax (primary)
  // plus the 'from' commit subject as a secondary hint.
  const triggerPrimary = rangeSyntax(selectedDiff, selectedTo);
  const fromCommit =
    selectedDiff && selectedDiff !== "working" ? diffs.find((d) => d.id === selectedDiff) : null;
  const triggerSecondary = fromCommit ? truncate(fromCommit.message, 60) : "";

  // Status line shown at the top of the open picker.
  const statusLine = pendingFrom ? (
    <div className="commit-picker-status commit-picker-status-pending">
      Pick the other end of{" "}
      <code>
        {shortHash(pendingFrom)}
        {"^\u2026?"}
      </code>{" "}
      (Esc to cancel)
    </div>
  ) : (
    <div className="commit-picker-status">
      Showing <code>{rangeSyntax(selectedDiff, selectedTo)}</code>
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
          {triggerSecondary && (
            <div className="commit-picker-trigger-secondary">{triggerSecondary}</div>
          )}
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
