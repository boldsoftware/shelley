import React, { useEffect, useMemo, useRef, useState } from "react";
import { ChevronDownIcon, ChevronRightIcon, FileIcon } from "lucide-react";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

// One row to render. `treePath` controls placement in the tree; we
// hand back `realPath` to the parent on selection so synthetic rows
// (e.g. commit messages) can pose as files in a pseudo-directory while
// the rest of the app keeps using the real path. `treePath` is an
// array of segments so callers can include `/` in a leaf label (e.g.
// commit subjects like `shelley/ui: foo`) without having those slashes
// silently turned into pseudo-directories.
export interface DiffFileTreeEntry {
  realPath: string;
  treePath: string[];
  status?: "added" | "modified" | "deleted";
  decoration?: string;
  decorationTitle?: string;
}

interface DiffFileTreeProps {
  entries: DiffFileTreeEntry[];
  selectedRealPath: string | null;
  onSelect: (realPath: string) => void;
}

// Internal tree node. Files carry the source entry; directories carry
// children. Directories with exactly one child directory are flattened
// at render time (e.g. `shelley/ui/src` collapses into a single row
// when there's nothing else at those levels).
interface DirNode {
  kind: "dir";
  name: string;
  path: string; // tree-path of this directory
  children: Node[];
}
interface FileNode {
  kind: "file";
  name: string;
  path: string; // tree-path of this file
  entry: DiffFileTreeEntry;
}
type Node = DirNode | FileNode;

// Internal directory/file `path` keys join segments with NUL — a
// character that can't appear in real file paths or commit subjects —
// so we can stash them in Sets/Maps without colliding with any
// legitimate `/` characters in the user-visible names.
const SEP = "\u0000";
const pathKey = (parts: string[]) => parts.join(SEP);

// Name of the synthetic top-level folder that commit-message rows are
// slotted under. Exported so the diff viewer builds `treePath`s with
// the exact same label the tree special-cases when sorting it first.
export const COMMIT_MESSAGES_DIR = "Commit messages";

// Child comparator: directories before files, the synthetic
// "Commit messages" folder always first, then alphabetical by name.
function compareNodes(a: Node, b: Node): number {
  if (a.kind !== b.kind) return a.kind === "dir" ? -1 : 1;
  const aCommit = a.kind === "dir" && a.name === COMMIT_MESSAGES_DIR;
  const bCommit = b.kind === "dir" && b.name === COMMIT_MESSAGES_DIR;
  if (aCommit !== bCommit) return aCommit ? -1 : 1;
  return a.name.localeCompare(b.name);
}

function buildTree(entries: DiffFileTreeEntry[]): DirNode {
  const root: DirNode = { kind: "dir", name: "", path: "", children: [] };
  for (const e of entries) {
    const parts = e.treePath;
    if (parts.length === 0) continue;
    let cur = root;
    for (let i = 0; i < parts.length - 1; i++) {
      const seg = parts[i];
      let child = cur.children.find((c) => c.kind === "dir" && c.name === seg) as
        | DirNode
        | undefined;
      if (!child) {
        child = {
          kind: "dir",
          name: seg,
          path: pathKey(parts.slice(0, i + 1)),
          children: [],
        };
        cur.children.push(child);
      }
      cur = child;
    }
    const leaf = parts[parts.length - 1];
    // Drop duplicates (same treePath used twice in the input).
    if (cur.children.some((c) => c.kind === "file" && c.name === leaf)) continue;
    cur.children.push({ kind: "file", name: leaf, path: pathKey(parts), entry: e });
  }
  // Sort: directories first ("Commit messages" always leading),
  // alphabetical within each kind.
  const sortRec = (d: DirNode) => {
    d.children.sort(compareNodes);
    for (const c of d.children) if (c.kind === "dir") sortRec(c);
  };
  sortRec(root);
  return root;
}

// Walk the (sorted) tree depth-first and return the real paths of the
// file rows in the exact order they render. The diff viewer uses this
// for next/previous-file navigation so it tracks what the user sees in
// the tree rather than the raw `files` order.
export function treeRealPathOrder(entries: DiffFileTreeEntry[]): string[] {
  const out: string[] = [];
  const walk = (d: DirNode) => {
    for (const c of d.children) {
      if (c.kind === "dir") walk(c);
      else out.push(c.entry.realPath);
    }
  };
  walk(buildTree(entries));
  return out;
}

// Walk into a directory, folding runs of single-child directories into
// a compound name like `shelley/ui/src`. Returns the visible name, the
// real directory node whose children we should render, and the set of
// intermediate directory paths the user has collapsed into this row.
function flatten(d: DirNode): { displayName: string; effective: DirNode; pathsCovered: string[] } {
  const pathsCovered = [d.path];
  let displayName = d.name;
  let effective = d;
  while (effective.children.length === 1 && effective.children[0].kind === "dir") {
    const only = effective.children[0] as DirNode;
    displayName += " / " + only.name;
    pathsCovered.push(only.path);
    effective = only;
  }
  return { displayName, effective, pathsCovered };
}

const STATUS_LABEL = {
  added: "Added",
  modified: "Modified",
  deleted: "Deleted",
} as const;

const CHEVRON_OPEN = <ChevronDownIcon className="size-3" aria-hidden="true" />;
const CHEVRON_CLOSED = <ChevronRightIcon className="size-3" aria-hidden="true" />;
const FILE_ICON = <FileIcon className="size-3" aria-hidden="true" />;

function statusLetter(
  s: DiffFileTreeEntry["status"],
): { letter: string; cls: string; label: string } | null {
  switch (s) {
    case "added":
      return {
        letter: "A",
        cls: "text-emerald-600 dark:text-emerald-500",
        label: STATUS_LABEL.added,
      };
    case "deleted":
      return {
        letter: "D",
        cls: "text-destructive",
        label: STATUS_LABEL.deleted,
      };
    case "modified":
      return {
        letter: "M",
        cls: "text-muted-foreground",
        label: STATUS_LABEL.modified,
      };
    default:
      return null;
  }
}

interface RowProps {
  depth: number;
  isSelected: boolean;
  onClick: () => void;
  icon: React.ReactNode;
  label: string;
  title?: string;
  decoration?: string;
  decorationTitle?: string;
  statusInfo?: { letter: string; cls: string; label: string } | null;
  rowRef?: React.Ref<HTMLButtonElement>;
  role?: string;
  ariaSelected?: boolean;
  ariaExpanded?: boolean;
}

function Row({
  depth,
  isSelected,
  onClick,
  icon,
  label,
  title,
  decoration,
  decorationTitle,
  statusInfo,
  rowRef,
  role,
  ariaSelected,
  ariaExpanded,
}: RowProps) {
  return (
    <button
      type="button"
      ref={rowRef}
      className={cn(
        "flex w-full min-w-0 items-center gap-1.5 rounded-md py-1 pr-2 text-left text-[13px] outline-none transition-colors",
        "focus-visible:ring-2 focus-visible:ring-ring/40",
        isSelected
          ? "bg-accent font-medium text-accent-foreground"
          : "text-foreground hover:bg-muted/60",
      )}
      style={{ paddingLeft: `calc(0.375rem + ${depth} * 0.85rem)` }}
      onClick={onClick}
      title={title}
      role={role}
      aria-selected={ariaSelected}
      aria-expanded={ariaExpanded}
    >
      <span
        className={cn(
          "inline-flex size-3.5 shrink-0 items-center justify-center",
          !isSelected && "text-muted-foreground",
        )}
      >
        {icon}
      </span>
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {decoration && (
        <span
          className={cn(
            "shrink-0 rounded px-1 py-px font-mono text-[10px]",
            isSelected
              ? "bg-accent-foreground/15 text-accent-foreground"
              : "bg-muted text-muted-foreground",
          )}
          title={decorationTitle}
        >
          {decoration}
        </span>
      )}
      {statusInfo && (
        <span
          className={cn(
            "w-3 shrink-0 text-center font-mono text-[11px] font-semibold",
            isSelected ? "text-accent-foreground" : statusInfo.cls,
          )}
          aria-label={statusInfo.label}
        >
          {statusInfo.letter}
        </span>
      )}
    </button>
  );
}

interface DirRowsProps {
  dir: DirNode;
  depth: number;
  selectedRealPath: string | null;
  onSelect: (path: string) => void;
  expanded: Set<string>;
  toggle: (paths: string[]) => void;
  selectedRowRef: React.Ref<HTMLButtonElement>;
  matchedPaths: Set<string> | null;
}

function DirRows({
  dir,
  depth,
  selectedRealPath,
  onSelect,
  expanded,
  toggle,
  selectedRowRef,
  matchedPaths,
}: DirRowsProps) {
  const out: React.ReactNode[] = [];
  for (const child of dir.children) {
    if (child.kind === "dir") {
      const { displayName, effective, pathsCovered } = flatten(child);
      // Hide directories whose subtree is fully filtered out by search.
      if (matchedPaths && !subtreeHasMatch(child, matchedPaths)) continue;
      // The whole flattened chain shares one open/closed state. We
      // consider it open if *every* covered path is in `expanded`,
      // and toggle flips the whole chain together — otherwise the
      // deepest path can collapse while ancestors stay expanded and
      // the row visually never closes.
      const isOpen = pathsCovered.every((p) => expanded.has(p));
      out.push(
        <Row
          key={`d:${child.path}`}
          depth={depth}
          isSelected={false}
          onClick={() => toggle(pathsCovered)}
          icon={isOpen ? CHEVRON_OPEN : CHEVRON_CLOSED}
          label={displayName}
          title={displayName}
          role="treeitem"
          ariaExpanded={isOpen}
        />,
      );
      if (isOpen) {
        out.push(
          <DirRows
            key={`c:${child.path}`}
            dir={effective}
            depth={depth + 1}
            selectedRealPath={selectedRealPath}
            onSelect={onSelect}
            expanded={expanded}
            toggle={toggle}
            selectedRowRef={selectedRowRef}
            matchedPaths={matchedPaths}
          />,
        );
      }
    } else {
      if (matchedPaths && !matchedPaths.has(child.path)) continue;
      const isSelected = child.entry.realPath === selectedRealPath;
      const statusInfo = statusLetter(child.entry.status);
      out.push(
        <Row
          key={`f:${child.path}`}
          rowRef={isSelected ? selectedRowRef : undefined}
          depth={depth}
          isSelected={isSelected}
          onClick={() => onSelect(child.entry.realPath)}
          icon={FILE_ICON}
          label={child.name}
          title={child.name}
          decoration={child.entry.decoration}
          decorationTitle={child.entry.decorationTitle}
          statusInfo={statusInfo}
          role="treeitem"
          ariaSelected={isSelected}
        />,
      );
    }
  }
  return <>{out}</>;
}

function subtreeHasMatch(node: Node, matchedPaths: Set<string>): boolean {
  if (node.kind === "file") return matchedPaths.has(node.path);
  for (const c of node.children) if (subtreeHasMatch(c, matchedPaths)) return true;
  return false;
}

// Render a simple file tree from a list of entries. Designed to feel
// at home in the existing diff-viewer sidebar (matching list-item
// hover/active styles), so we get something simple and predictable
// instead of fighting a third-party tree's truncation and theming.
function DiffFileTree({ entries, selectedRealPath, onSelect }: DiffFileTreeProps) {
  const tree = useMemo(() => buildTree(entries), [entries]);
  // Track all directory paths so we can default them open. We keep the
  // expansion set stable across re-renders so toggles persist while the
  // user navigates between commits/files.
  const allDirPaths = useMemo(() => {
    const out: string[] = [];
    const walk = (d: DirNode) => {
      for (const c of d.children) {
        if (c.kind === "dir") {
          out.push(c.path);
          walk(c);
        }
      }
    };
    walk(tree);
    return out;
  }, [tree]);
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set(allDirPaths));
  // When the directory set changes (new commit, range flip), default-
  // expand any newly-introduced directories without forcing back open
  // ones the user has explicitly closed in this session. We use the
  // functional setExpanded form so the effect doesn't need to depend
  // on `expanded` (which would re-run it on every toggle).
  const seenDirsRef = useRef<Set<string>>(new Set(allDirPaths));
  useEffect(() => {
    const fresh: string[] = [];
    for (const p of allDirPaths) {
      if (!seenDirsRef.current.has(p)) {
        fresh.push(p);
        seenDirsRef.current.add(p);
      }
    }
    if (fresh.length === 0) return;
    setExpanded((prev) => {
      const next = new Set(prev);
      for (const p of fresh) next.add(p);
      return next;
    });
  }, [allDirPaths]);

  // Toggle a chain of directory paths together. Flattened chains
  // expose one open/closed surface to the user, so they need to flip
  // as a unit; otherwise the deepest path can collapse while ancestors
  // remain open and the row stays visually expanded.
  const toggle = (paths: string[]) =>
    setExpanded((prev) => {
      const allOpen = paths.every((p) => prev.has(p));
      const next = new Set(prev);
      if (allOpen) for (const p of paths) next.delete(p);
      else for (const p of paths) next.add(p);
      return next;
    });

  // Search: case-insensitive substring match on the basename.
  const [query, setQuery] = useState("");
  const matchedPaths = useMemo<Set<string> | null>(() => {
    const q = query.trim().toLowerCase();
    if (!q) return null;
    const m = new Set<string>();
    const walk = (d: DirNode) => {
      for (const c of d.children) {
        if (c.kind === "file") {
          if (c.name.toLowerCase().includes(q)) m.add(c.path);
        } else {
          walk(c);
        }
      }
    };
    walk(tree);
    return m;
  }, [tree, query]);

  // When searching, force-expand every ancestor so matches are visible.
  const effectiveExpanded = useMemo(() => {
    if (!matchedPaths) return expanded;
    const out = new Set(expanded);
    const walk = (d: DirNode, ancestors: string[]) => {
      for (const c of d.children) {
        if (c.kind === "file") {
          if (matchedPaths.has(c.path)) for (const a of ancestors) out.add(a);
        } else {
          walk(c, [...ancestors, c.path]);
        }
      }
    };
    walk(tree, []);
    return out;
  }, [tree, matchedPaths, expanded]);

  const selectedRowRef = useRef<HTMLButtonElement | null>(null);
  // Keep the selected row in view when the parent moves the selection
  // (e.g. via file-nav arrows).
  useEffect(() => {
    selectedRowRef.current?.scrollIntoView({ block: "nearest" });
  }, [selectedRealPath]);

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-1">
      <div className="shrink-0 px-1">
        <Input
          type="text"
          placeholder="Search…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          aria-label="Filter files"
        />
      </div>
      <div
        className="min-h-0 flex-1 overflow-x-hidden overflow-y-auto px-1 pb-1"
        role="tree"
        aria-label="Files"
      >
        <DirRows
          dir={tree}
          depth={0}
          selectedRealPath={selectedRealPath}
          onSelect={onSelect}
          expanded={effectiveExpanded}
          toggle={toggle}
          selectedRowRef={selectedRowRef}
          matchedPaths={matchedPaths}
        />
      </div>
    </div>
  );
}

export default DiffFileTree;
