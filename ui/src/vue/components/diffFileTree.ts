// Framework-agnostic tree logic shared by the Vue SFCs (DiffFileTree.vue /
// DiffTreeRows.vue) and the diff viewer (realPathOrder).

// One row to render. `treePath` controls placement in the tree; we hand back
// `realPath` to the parent on selection so synthetic rows (e.g. commit
// messages) can pose as files in a pseudo-directory while the rest of the app
// keeps using the real path. `treePath` is an array of segments so callers can
// include `/` in a leaf label without having those slashes silently turned
// into pseudo-directories.
export interface DiffFileTreeEntry {
  realPath: string;
  treePath: string[];
  status?: "added" | "modified" | "deleted";
  additions?: number;
  deletions?: number;
  decoration?: string;
  decorationTitle?: string;
  // Number of commit highlights on this file, shown as a badge.
  highlightCount?: number;
}

// Internal tree node. Files carry the source entry; directories carry
// children. Directories with exactly one child directory are flattened at
// render time.
export interface DirNode {
  kind: "dir";
  name: string;
  path: string; // tree-path of this directory
  children: Node[];
}
export interface FileNode {
  kind: "file";
  name: string;
  path: string; // tree-path of this file
  entry: DiffFileTreeEntry;
}
export type Node = DirNode | FileNode;

// Internal directory/file `path` keys join segments with NUL so we can stash
// them in Sets/Maps without colliding with legitimate `/` characters.
const SEP = "\u0000";
const pathKey = (parts: string[]) => parts.join(SEP);

// Name of the synthetic top-level folder that commit-message rows are slotted
// under. Exported so the diff viewer builds `treePath`s with the exact same
// label the tree special-cases when sorting it first.
export const COMMIT_MESSAGES_DIR = "Commit messages";

// Child comparator: directories before files, the synthetic "Commit messages"
// folder always first, then alphabetical by name.
function compareNodes(a: Node, b: Node): number {
  if (a.kind !== b.kind) return a.kind === "dir" ? -1 : 1;
  const aCommit = a.kind === "dir" && a.name === COMMIT_MESSAGES_DIR;
  const bCommit = b.kind === "dir" && b.name === COMMIT_MESSAGES_DIR;
  if (aCommit !== bCommit) return aCommit ? -1 : 1;
  return a.name.localeCompare(b.name);
}

export function buildTree(entries: DiffFileTreeEntry[]): DirNode {
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
  // Sort: directories first ("Commit messages" always leading), alphabetical
  // within each kind.
  const sortRec = (d: DirNode) => {
    d.children.sort(compareNodes);
    for (const c of d.children) if (c.kind === "dir") sortRec(c);
  };
  sortRec(root);
  return root;
}

// In an ordered list (commit highlights rank the files), commit messages keep
// their folder at the top and every other file follows in entry order.
export function isCommitMessageEntry(entry: DiffFileTreeEntry): boolean {
  return entry.treePath.length > 1 && entry.treePath[0] === COMMIT_MESSAGES_DIR;
}

// A folder row as the alphabetical tree renders it (single-child runs folded),
// and a file with the chain of folder rows above it.
interface FolderRow {
  path: string;
  label: string;
  isPath: boolean;
  pathsCovered: string[];
}
interface PlacedFile {
  node: FileNode;
  chain: FolderRow[];
}

// Files in the order they render: the alphabetical tree's depth-first order,
// or, if `ordered`, commit messages in that order followed by the rest in
// entry order.
function placeFiles(tree: DirNode, entries: DiffFileTreeEntry[], ordered: boolean): PlacedFile[] {
  const out: PlacedFile[] = [];
  const walk = (d: DirNode, chain: FolderRow[]) => {
    for (const c of d.children) {
      if (c.kind === "file") {
        out.push({ node: c, chain });
        continue;
      }
      const { displayName, effective, pathsCovered } = flatten(c);
      const row = {
        path: c.path,
        label: displayName,
        isPath: c.name !== COMMIT_MESSAGES_DIR,
        pathsCovered,
      };
      walk(effective, [...chain, row]);
    }
  };
  walk(tree, []);
  if (!ordered) return out;
  const byEntry = new Map(out.map((p) => [p.node.entry, p]));
  return [
    ...out.filter((p) => isCommitMessageEntry(p.node.entry)),
    ...entries.filter((e) => !isCommitMessageEntry(e)).flatMap((e) => byEntry.get(e) ?? []),
  ];
}

// The real paths of the file rows in the order they render. The diff viewer
// uses this for next/previous-file navigation.
export function realPathOrder(entries: DiffFileTreeEntry[], ordered: boolean): string[] {
  return placeFiles(buildTree(entries), entries, ordered).map((p) => p.node.entry.realPath);
}

// The rows to render. Each file sits under its folder rows; walking the files
// in order, folder rows already open above (the common prefix with the
// previous file's chain) are shared and the rest are emitted again, so in an
// ordered list a folder repeats wherever its files aren't contiguous. A
// collapsed folder still shows its headers but hides the files below them.
// Searching keeps only matching files and opens every folder above them.
export function buildRows(
  entries: DiffFileTreeEntry[],
  ordered: boolean,
  collapsed: Set<string>,
  query: string,
  selectedRealPath: string | null,
): RenderedRow[] {
  const tree = buildTree(entries);
  const matched = matchTreePaths(tree, query);
  const out: RenderedRow[] = [];
  const headerCount = new Map<string, number>();
  let open: FolderRow[] = [];
  for (const { node, chain } of placeFiles(tree, entries, ordered)) {
    if (matched && !matched.has(node.path)) continue;
    const closedAt = matched
      ? -1
      : chain.findIndex((f) => f.pathsCovered.some((p) => collapsed.has(p)));
    const shown = closedAt < 0 ? chain : chain.slice(0, closedAt + 1);
    let depth = 0;
    while (depth < open.length && depth < shown.length && open[depth] === shown[depth]) depth++;
    for (; depth < shown.length; depth++) {
      const f = shown[depth];
      const n = headerCount.get(f.path) ?? 0;
      headerCount.set(f.path, n + 1);
      out.push({
        kind: "dir",
        key: `d:${f.path}:${n}`,
        depth,
        isOpen: depth !== closedAt,
        label: f.label,
        isPath: f.isPath,
        pathsCovered: f.pathsCovered,
      });
    }
    open = shown;
    if (closedAt >= 0) continue;
    const entry = node.entry;
    out.push({
      kind: "file",
      key: `f:${node.path}`,
      depth: chain.length,
      isSelected: entry.realPath === selectedRealPath,
      label: node.name,
      isPath: !isCommitMessageEntry(entry),
      realPath: entry.realPath,
      decoration: entry.decoration,
      decorationTitle: entry.decorationTitle,
      statusInfo: statusLetter(entry.status),
      additions: entry.additions,
      deletions: entry.deletions,
      highlightCount: entry.highlightCount,
    });
  }
  return out;
}

// Walk into a directory, folding runs of single-child directories into a
// compound name like `shelley / ui / src`.
export function flatten(d: DirNode): {
  displayName: string;
  effective: DirNode;
  pathsCovered: string[];
} {
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

export interface StatusInfo {
  letter: string;
  cls: string;
  label: string;
}

export function statusLetter(s: DiffFileTreeEntry["status"]): StatusInfo | null {
  switch (s) {
    case "added":
      return { letter: "A", cls: "diff-tree-status-added", label: STATUS_LABEL.added };
    case "deleted":
      return { letter: "D", cls: "diff-tree-status-deleted", label: STATUS_LABEL.deleted };
    case "modified":
      return { letter: "M", cls: "diff-tree-status-modified", label: STATUS_LABEL.modified };
    default:
      return null;
  }
}

// Lowercase and drop spaces around "/" (see matchTreePaths).
export const normalizeSearch = (s: string) => s.toLowerCase().replace(/\s*\/\s*/g, "/");

// Search: case-insensitive substring match against each file's full tree
// path (segments joined with "/"), so a query hits directory names as well
// as basenames ("doc" finds docs/content/vm-backups.md) and can span a
// separator ("content/vm"). The synthetic commit-messages folder is not part
// of the searchable path; otherwise "message" or "com" would list every
// commit. Spaces around "/" are dropped on both sides so the folded-directory
// label as displayed ("docs / content") matches, as does a commit subject
// containing " / ". Returns null for a blank query (no filtering), otherwise
// the set of matching file path keys.
export function matchTreePaths(tree: DirNode, query: string): Set<string> | null {
  const q = normalizeSearch(query.trim());
  if (!q) return null;
  const m = new Set<string>();
  const walk = (d: DirNode, prefix: string) => {
    for (const c of d.children) {
      const full = prefix ? `${prefix}/${c.name}` : c.name;
      if (c.kind === "file") {
        if (normalizeSearch(full).includes(q)) m.add(c.path);
      } else if (d === tree && c.name === COMMIT_MESSAGES_DIR) {
        walk(c, "");
      } else {
        walk(c, full);
      }
    }
  };
  walk(tree, "");
  return m;
}

// A pre-computed flat row produced by buildRows and rendered by
// DiffTreeRows.vue, so the Vue side avoids a recursive component.
export type RenderedRow =
  | {
      kind: "dir";
      key: string;
      depth: number;
      isPath: boolean;
      isOpen: boolean;
      label: string;
      pathsCovered: string[];
    }
  | {
      kind: "file";
      key: string;
      depth: number;
      isPath: boolean;
      isSelected: boolean;
      label: string;
      realPath: string;
      decoration?: string;
      decorationTitle?: string;
      statusInfo: StatusInfo | null;
      additions?: number;
      deletions?: number;
      highlightCount?: number;
    };
