import type { GitDiffInfo } from "../../types";

export interface DiffSelection {
  selectedDiff: string;
  selectedTo: "working" | "self";
}

export interface WorkingChangesStatus {
  label: string;
  description: string;
  clean: boolean;
}

export function workingChangesStatus(diff: GitDiffInfo): WorkingChangesStatus {
  if (diff.filesCount === 0) {
    return { label: "Clean", description: "No working changes", clean: true };
  }
  const noun = diff.filesCount === 1 ? "file" : "files";
  return {
    label: `${diff.filesCount} ${noun}`,
    description: `${diff.filesCount} changed ${noun}`,
    clean: false,
  };
}

export function defaultDiffSelection(
  diffs: GitDiffInfo[],
  initialCommit?: string,
): DiffSelection | null {
  if (initialCommit) {
    const matchingDiff = diffs.find(
      (diff) => diff.id === initialCommit || diff.id.startsWith(initialCommit),
    );
    if (matchingDiff) {
      return {
        selectedDiff: matchingDiff.id,
        selectedTo: matchingDiff.id === "working" ? "working" : "self",
      };
    }
  }

  const working = diffs.find((diff) => diff.id === "working");
  const commits = diffs.filter((diff) => diff.id !== "working");
  const topCommit = commits[0];

  if (working?.filesCount === 0 && topCommit?.hasHighlights) {
    return { selectedDiff: topCommit.id, selectedTo: "self" };
  }

  const mergeBaseIndex = commits.findIndex((diff) => diff.isMergeBase);
  const topOfBranch = mergeBaseIndex > 0 ? commits[mergeBaseIndex - 1] : undefined;
  if (topOfBranch) {
    return { selectedDiff: topOfBranch.id, selectedTo: "working" };
  }
  if (working && working.filesCount > 0) {
    return { selectedDiff: working.id, selectedTo: "working" };
  }
  if (topCommit) {
    return { selectedDiff: topCommit.id, selectedTo: "self" };
  }
  if (working) {
    return { selectedDiff: working.id, selectedTo: "working" };
  }
  return null;
}
