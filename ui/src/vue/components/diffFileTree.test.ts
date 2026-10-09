import {
  buildTree,
  COMMIT_MESSAGES_DIR,
  matchTreePaths,
  type DiffFileTreeEntry,
  type DirNode,
} from "./diffFileTree";

function assertEqual(actual: unknown, expected: unknown, message: string): void {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(
      `${message}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`,
    );
  }
}

function run(name: string, fn: () => void): void {
  fn();
  console.log(`✓ ${name}`);
}

const file = (p: string): DiffFileTreeEntry => ({ realPath: p, treePath: p.split("/") });
const entries: DiffFileTreeEntry[] = [
  { realPath: "commit-msg:abc", treePath: [COMMIT_MESSAGES_DIR, "Add VM backups docs"] },
  { realPath: "commit-msg:def", treePath: [COMMIT_MESSAGES_DIR, "ui: A / B testing toggle"] },
  file("docs/content/vm-backups.md"),
  file("exed/backup.go"),
  file("README.md"),
  file("shelley/server/messages.go"),
];
const tree = buildTree(entries);

// Matched files, as sorted realPaths for readable asserts.
const realPathByKey = new Map<string, string>();
const collect = (d: DirNode) => {
  for (const c of d.children) {
    if (c.kind === "file") realPathByKey.set(c.path, c.entry.realPath);
    else collect(c);
  }
};
collect(tree);
const matches = (q: string) => {
  const m = matchTreePaths(tree, q);
  return m === null ? null : [...m].map((k) => realPathByKey.get(k)).sort();
};

run("empty query matches nothing (no filter)", () => {
  assertEqual(matches(""), null, "empty");
  assertEqual(matches("   "), null, "whitespace");
});

run("matches directory segments, not just the basename", () => {
  assertEqual(matches("doc"), ["commit-msg:abc", "docs/content/vm-backups.md"], "doc");
  assertEqual(matches("content"), ["docs/content/vm-backups.md"], "content");
});

run("matches across a path separator", () => {
  assertEqual(matches("content/vm"), ["docs/content/vm-backups.md"], "content/vm");
  assertEqual(matches("exed/back"), ["exed/backup.go"], "exed/back");
});

run("still matches basenames, case-insensitively", () => {
  assertEqual(
    matches("BACKUP"),
    ["commit-msg:abc", "docs/content/vm-backups.md", "exed/backup.go"],
    "BACKUP",
  );
  assertEqual(matches("readme"), ["README.md"], "readme");
});

run("no match yields an empty set", () => {
  assertEqual(matches("zzz"), [], "zzz");
});

run("the synthetic commit-messages folder name is not searchable", () => {
  assertEqual(matches("message"), ["shelley/server/messages.go"], "message");
  assertEqual(matches("commit"), [], "commit");
});

run("spaces around a slash match the folded-directory label", () => {
  assertEqual(matches("docs / content"), ["docs/content/vm-backups.md"], "docs / content");
});

run('a commit subject containing " / " matches however the slash is spaced', () => {
  assertEqual(matches("A / B testing"), ["commit-msg:def"], "A / B testing");
  assertEqual(matches("a/b"), ["commit-msg:def"], "a/b");
});
