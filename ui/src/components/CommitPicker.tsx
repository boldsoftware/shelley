import React, { useEffect, useMemo, useRef, useState } from "react";
import { ChevronDownIcon, XIcon } from "lucide-react";
import { GitDiffInfo } from "../types";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

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
  return s.slice(0, Math.max(0, n - 1)) + "…";
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
  if (!selectedDiff) return "Choose…";
  if (selectedDiff === "working") return "Working Changes";
  const from = commitLabel(diffs, selectedDiff);
  if (selectedTo === "self") return `${from} (Single Commit)`;
  return `${from} → Now`;
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
  const opts: {
    value: "self" | "working";
    label: string;
  }[] = [
    { value: "self", label: "Single commit" },
    { value: "working", label: "Through working tree" },
  ];
  return (
    <div
      className="inline-flex rounded-lg border border-border bg-muted/40 p-0.5"
      role="radiogroup"
      aria-label="Diff range"
    >
      {opts.map((o) => {
        const active = selectedTo === o.value;
        return (
          <button
            key={o.value}
            type="button"
            className={cn(
              "inline-flex items-center gap-1.5 rounded-md px-2.5 py-1 text-xs font-medium transition-colors",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
              "disabled:pointer-events-none disabled:opacity-50",
              active
                ? "bg-background text-foreground shadow-sm"
                : "text-muted-foreground hover:text-foreground",
            )}
            onClick={() => {
              if (selectedDiff && selectedDiff !== "working") onChange(selectedDiff, o.value);
            }}
            disabled={disabled}
            role="radio"
            aria-checked={active}
          >
            <span
              className={cn(
                "size-2.5 shrink-0 rounded-full border",
                active ? "border-primary bg-primary" : "border-muted-foreground/50",
              )}
              aria-hidden="true"
            />
            <span>{o.label}</span>
          </button>
        );
      })}
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
    const chipBase =
      "inline-flex items-center rounded-md border px-1.5 py-px font-mono text-[10px] leading-tight";
    const chips: React.ReactNode[] = refs.map((ref) => {
      const isHead = ref === "HEAD";
      const isRemote = ref.includes("/");
      return (
        <span
          key={ref}
          className={cn(
            chipBase,
            isHead && "border-amber-500/40 bg-amber-500/15 text-amber-700 dark:text-amber-400",
            isRemote && "border-blue-500/40 bg-blue-500/15 text-blue-700 dark:text-blue-400",
            !isHead &&
              !isRemote &&
              "border-border bg-muted text-muted-foreground",
          )}
        >
          {ref}
        </span>
      );
    });
    if (showMergeBase) {
      chips.push(
        <span
          key="__mergebase"
          className={cn(
            chipBase,
            "border-emerald-500/40 bg-emerald-500/15 text-emerald-700 dark:text-emerald-400",
          )}
          title="Merge-base with @{upstream}"
        >
          merge-base
        </span>,
      );
    }
    if (chips.length === 0) return null;
    return <span className="flex flex-wrap items-center gap-1">{chips}</span>;
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

  // Shared classes for a clickable row button. `isFrom` is the explicitly
  // selected commit; `inRange` rows sit between it and the working tree.
  const rowMainClass = (isFrom: boolean, inRange: boolean) =>
    cn(
      "commit-picker-row-main flex w-full items-start gap-2 rounded-md px-2 py-1.5 text-left transition-colors",
      "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
      isFrom
        ? "bg-primary/15 text-foreground"
        : inRange
          ? "bg-muted/60 text-foreground hover:bg-muted"
          : "text-foreground hover:bg-muted",
    );

  const renderCommitRow = (d: GitDiffInfo, idx: number) => {
    const isFrom = d.id === selectedDiff;
    const inRange = !isFrom && rowInRange(idx);
    const stats = `+${d.additions}/-${d.deletions}`;
    const hash = shortHash(d.id);

    return (
      <div
        key={d.id}
        className={cn("commit-picker-row", isFrom && "commit-picker-row-from")}
      >
        <button
          type="button"
          className={rowMainClass(isFrom, inRange)}
          onClick={() => pickCommit(d.id)}
        >
          <div
            className={cn(
              "mt-0.5 w-3 shrink-0 text-center font-mono text-xs leading-5",
              isFrom ? "text-primary" : "text-muted-foreground",
            )}
            aria-hidden="true"
          >
            {isFrom ? "●" : inRange ? "│" : ""}
          </div>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-1.5">
              {renderRefs(d)}
              <span className="min-w-0 truncate text-sm">{d.message}</span>
            </div>
            <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted-foreground">
              <span className="font-mono">{hash}</span>
              <span className="truncate">{d.author}</span>
              <span className="font-mono">
                {d.filesCount} files {"·"} {stats}
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
    <div className="flex flex-col gap-0.5 overflow-y-auto" onKeyDown={onListKeyDown}>
      {workingDiff && (
        <div
          className={cn(
            "commit-picker-row",
            selectedDiff === "working" && "commit-picker-row-from",
          )}
        >
          <button
            type="button"
            className={rowMainClass(
              selectedDiff === "working",
              workingInRange && selectedDiff !== "working",
            )}
            onClick={pickWorking}
          >
            <div
              className={cn(
                "mt-0.5 w-3 shrink-0 text-center font-mono text-xs leading-5",
                selectedDiff === "working" ? "text-primary" : "text-muted-foreground",
              )}
              aria-hidden="true"
            >
              {selectedDiff === "working" ? "●" : workingInRange ? "│" : ""}
            </div>
            <div className="min-w-0 flex-1">
              <div className="text-sm font-medium">Working Changes</div>
              <div className="mt-0.5 text-xs text-muted-foreground">
                <span className="font-mono">
                  {workingDiff.filesCount} files {"·"} +{workingDiff.additions}/-
                  {workingDiff.deletions}
                </span>
              </div>
            </div>
          </button>
        </div>
      )}
      {commitDiffs.map(renderCommitRow)}
      {commitDiffs.length === 0 && !workingDiff && (
        <div className="px-2 py-6 text-center text-sm text-muted-foreground">
          No commits or working changes.
        </div>
      )}
    </div>
  );

  const triggerPrimary = rangeSyntax(diffs, selectedDiff, selectedTo);

  const statusLine = (
    <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border pb-3">
      <span className="text-xs text-muted-foreground">
        Showing{" "}
        <code className="rounded bg-muted px-1 py-0.5 font-mono text-foreground">
          {rangeSyntax(diffs, selectedDiff, selectedTo)}
        </code>
      </span>
      {rangeToggle}
    </div>
  );

  return (
    <div className="relative inline-block">
      <Button
        ref={triggerRef}
        type="button"
        variant="outline"
        size="sm"
        className="max-w-[18rem] justify-between gap-2 font-normal"
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-label="Commit"
      >
        <code className="min-w-0 truncate font-mono text-xs">{triggerPrimary}</code>
        <ChevronDownIcon className="size-3.5 shrink-0 opacity-60" aria-hidden="true" />
      </Button>

      {open && isMobile && (
        <div
          className="fixed inset-0 z-50 flex items-end justify-center bg-black/50 sm:items-center"
          onClick={() => setOpen(false)}
        >
          <div
            ref={popoverRef}
            className="commit-picker-modal flex max-h-[80vh] w-full max-w-lg flex-col gap-3 rounded-t-2xl border border-border bg-popover p-4 text-popover-foreground shadow-lg sm:rounded-2xl"
            onClick={(e) => e.stopPropagation()}
            role="dialog"
            aria-label="Choose commit"
          >
            <div className="flex items-center justify-between gap-2">
              <span className="text-sm font-medium">Choose commit</span>
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                onClick={() => setOpen(false)}
                aria-label="Close"
              >
                <XIcon className="size-4" aria-hidden="true" />
              </Button>
            </div>
            {statusLine}
            {list}
          </div>
        </div>
      )}

      {open && !isMobile && (
        <div
          ref={popoverRef}
          className="commit-picker-popover absolute left-0 z-50 mt-2 flex max-h-[70vh] w-[28rem] max-w-[90vw] flex-col gap-3 rounded-2xl border border-border bg-popover p-4 text-popover-foreground shadow-lg"
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
