// Framework-agnostic tree logic extracted from components/DiffFileTree.tsx so
// both the Vue SFC (DiffFileTree.vue / DiffTreeRows.vue) and the diff viewer
// (treeRealPathOrder) can share it. Keeps the COMMIT_MESSAGES_DIR ordering
// and flattening semantics identical to the React original.

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

// Walk the (sorted) tree depth-first and return the real paths of the file
// rows in the exact order they render. The diff viewer uses this for
// next/previous-file navigation so it tracks what the user sees in the tree
// rather than the raw `files` order.
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

export function subtreeHasMatch(node: Node, matchedPaths: Set<string>): boolean {
  if (node.kind === "file") return matchedPaths.has(node.path);
  for (const c of node.children) if (subtreeHasMatch(c, matchedPaths)) return true;
  return false;
}

// A pre-computed flat row produced by DiffFileTree.vue and rendered by
// DiffTreeRows.vue. Mirrors the props the React <Row> took, but flattened so
// the Vue side avoids a recursive component.
export type RenderedRow =
  | {
      kind: "dir";
      key: string;
      depth: number;
      isOpen: boolean;
      label: string;
      pathsCovered: string[];
    }
  | {
      kind: "file";
      key: string;
      depth: number;
      isSelected: boolean;
      label: string;
      realPath: string;
      decoration?: string;
      decorationTitle?: string;
      statusInfo: StatusInfo | null;
      additions?: number;
      deletions?: number;
    };
