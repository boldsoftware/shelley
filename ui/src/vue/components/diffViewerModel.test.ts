import type { GitDiffInfo } from "../../types";
import { defaultDiffSelection, workingChangesStatus } from "./diffViewerModel";

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

function diff(overrides: Partial<GitDiffInfo>): GitDiffInfo {
  return {
    id: "commit",
    message: "Commit",
    author: "Test",
    timestamp: "2026-09-08T00:00:00Z",
    filesCount: 1,
    additions: 1,
    deletions: 0,
    ...overrides,
  };
}

run("a clean working tree defaults to top commit highlights", () => {
  const selection = defaultDiffSelection([
    diff({ id: "working", message: "Working Changes", filesCount: 0, additions: 0 }),
    diff({ id: "top", message: "Top commit", hasHighlights: true }),
    diff({ id: "merge-base", message: "Merge base", isMergeBase: true }),
  ]);
  assertEqual(selection, { selectedDiff: "top", selectedTo: "self" }, "selection");
});

run("working changes keep the through-working-tree branch default", () => {
  const selection = defaultDiffSelection([
    diff({ id: "working", message: "Working Changes", filesCount: 1 }),
    diff({ id: "top", message: "Top commit", hasHighlights: true }),
    diff({ id: "oldest-local", message: "Oldest local" }),
    diff({ id: "merge-base", message: "Merge base", isMergeBase: true }),
  ]);
  assertEqual(selection, { selectedDiff: "oldest-local", selectedTo: "working" }, "selection");
});

run("an explicit commit remains authoritative", () => {
  const selection = defaultDiffSelection(
    [
      diff({ id: "working", message: "Working Changes", filesCount: 0 }),
      diff({ id: "top", message: "Top commit", hasHighlights: true }),
      diff({ id: "chosen", message: "Chosen commit" }),
    ],
    "chos",
  );
  assertEqual(selection, { selectedDiff: "chosen", selectedTo: "self" }, "selection");
});

run("working changes status is explicit", () => {
  assertEqual(
    workingChangesStatus(diff({ id: "working", filesCount: 0 })),
    { label: "Clean", description: "No working changes", clean: true },
    "clean status",
  );
  assertEqual(
    workingChangesStatus(diff({ id: "working", filesCount: 2 })),
    { label: "2 files", description: "2 changed files", clean: false },
    "dirty status",
  );
});
