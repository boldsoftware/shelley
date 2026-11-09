import {
  buildTree,
  COMMIT_MESSAGES_DIR,
  buildRows,
  matchTreePaths,
  realPathOrder,
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

run("ordered lists keep commit messages first and files in entry order", () => {
  const ranked = [
    entries[0],
    entries[1],
    file("shelley/server/messages.go"),
    file("README.md"),
    file("exed/backup.go"),
    file("docs/content/vm-backups.md"),
  ];
  assertEqual(
    realPathOrder(ranked, true),
    [
      "commit-msg:abc",
      "commit-msg:def",
      "shelley/server/messages.go",
      "README.md",
      "exed/backup.go",
      "docs/content/vm-backups.md",
    ],
    "ordered",
  );
  assertEqual(
    realPathOrder(ranked, false).slice(2),
    ["docs/content/vm-backups.md", "exed/backup.go", "shelley/server/messages.go", "README.md"],
    "tree order ignores entry order",
  );
});

// Rows as indented text: folders end in "/", or " +" when collapsed.
const show = (
  es: DiffFileTreeEntry[],
  ordered: boolean,
  opts: { collapsed?: string[]; query?: string } = {},
) =>
  buildRows(es, ordered, new Set(opts.collapsed), opts.query ?? "", null).map(
    (r) => "  ".repeat(r.depth) + r.label + (r.kind === "file" ? "" : r.isOpen ? "/" : " +"),
  );

const spec1 = [entries[0], file("server/model.go"), file("README.md"), file("server/handler.go")];

run("ordered rows repeat a folder whose files aren't contiguous in rank", () => {
  assertEqual(
    show(spec1, true),
    [
      "Commit messages/",
      "  Add VM backups docs",
      "server/",
      "  model.go",
      "README.md",
      "server/",
      "  handler.go",
    ],
    "rows",
  );
  assertEqual(
    realPathOrder(spec1, true),
    ["commit-msg:abc", "server/model.go", "README.md", "server/handler.go"],
    "nav order follows the rows",
  );
});

run("ordered rows share a common folder prefix", () => {
  assertEqual(
    show([file("a/b/x.go"), file("a/c/y.go")], true),
    ["a/", "  b/", "    x.go", "  c/", "    y.go"],
    "rows",
  );
});

run("ordered rows fold single-child folders like the alphabetical tree", () => {
  const es = [file("cmd/hello/main.go"), file("README.md"), file("cmd/hello/x.go")];
  assertEqual(
    show(es, true),
    ["cmd / hello/", "  main.go", "README.md", "cmd / hello/", "  x.go"],
    "ordered",
  );
  assertEqual(
    show(es, false),
    ["cmd / hello/", "  main.go", "  x.go", "README.md"],
    "alphabetical",
  );
});

run("repeated folder rows have unique keys", () => {
  const keys = buildRows(spec1, true, new Set(), "", null).map((r) => r.key);
  assertEqual(new Set(keys).size, keys.length, "unique keys");
});

run("collapsing a folder hides its files under every header for it", () => {
  assertEqual(
    show(spec1, true, { collapsed: ["server"] }),
    ["Commit messages/", "  Add VM backups docs", "server +", "README.md", "server +"],
    "server collapsed",
  );
  const es = [file("a/b/x.go"), file("README.md"), file("a/c/y.go"), file("a/b/z.go")];
  const b = buildRows(es, true, new Set(), "", null).find((r) => r.label === "b")!;
  assertEqual(
    show(es, true, { collapsed: b.kind === "dir" ? b.pathsCovered : [] }),
    ["a/", "  b +", "README.md", "a/", "  c/", "    y.go", "  b +"],
    "a/b collapsed",
  );
});

run("searching an ordered list keeps the folders of the matches", () => {
  assertEqual(show(spec1, true, { query: "model" }), ["server/", "  model.go"], "model");
  assertEqual(
    show(spec1, true, { query: "server", collapsed: ["server"] }),
    ["server/", "  model.go", "  handler.go"],
    "search opens collapsed folders",
  );
});

run("unordered rows are the alphabetical tree, folders first", () => {
  assertEqual(
    show(spec1, false),
    [
      "Commit messages/",
      "  Add VM backups docs",
      "server/",
      "  handler.go",
      "  model.go",
      "README.md",
    ],
    "rows",
  );
});
