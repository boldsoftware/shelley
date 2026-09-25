// Helpers for the commit tour's "show full file" mode, kept free of
// @pierre/diffs imports (whose entry point needs a browser) so they can be
// unit tested under node.
import type { GitFileDiff } from "../../types";
import type { TourDiffSide } from "../composables/tourComments";

// Split contents into lines numbered the way @pierre/diffs numbers them: a
// trailing newline terminates the last line rather than starting an empty one.
function fileLines(contents: string): string[] {
  if (contents === "") return [];
  const lines = contents.split("\n");
  if (lines[lines.length - 1] === "") lines.pop();
  return lines;
}

// Text of a 1-based line on one side of the diff, or null when out of range.
// Mirrors patchLineText for lines that only exist in the full file.
export function fileLineText(
  contents: GitFileDiff,
  side: TourDiffSide,
  lineNumber: number,
): string | null {
  const lines = fileLines(side === "deletions" ? contents.oldContent : contents.newContent);
  if (lineNumber < 1 || lineNumber > lines.length) return null;
  return lines[lineNumber - 1];
}

export interface HunkExtent {
  oldStart: number;
  oldCount: number;
  newStart: number;
  newCount: number;
}

// Whether both sides of the fetched file are long enough to contain every
// hunk of the chunk's patch. The server hands back empty content when a side
// cannot be read (a submodule gitlink, a path it could not resolve), and a
// whole-file diff built from that would misreport the change as the entire
// file being added or removed.
export function patchFitsContents(hunks: readonly HunkExtent[], contents: GitFileDiff): boolean {
  const oldLines = fileLines(contents.oldContent).length;
  const newLines = fileLines(contents.newContent).length;
  return hunks.every(
    (hunk) =>
      hunk.oldStart + hunk.oldCount - 1 <= oldLines &&
      hunk.newStart + hunk.newCount - 1 <= newLines,
  );
}

export interface FullFilePaths {
  oldPath: string | null;
  newPath: string | null;
  fileLabel: string;
}

// Names for both sides of a whole-file diff. Either side may be missing from
// the patch headers (new, deleted, or header-less patches), and the name drives
// syntax highlighting, so fall back to whatever name we do have.
export function fullFileNames(paths: FullFilePaths): { old: string; new: string } {
  const fallback = paths.newPath || paths.oldPath || paths.fileLabel;
  return { old: paths.oldPath || fallback, new: paths.newPath || fallback };
}

export interface ChangeRowGeometry {
  type: string;
  line: string;
  top: number;
  bottom: number;
}

export interface ChangeLineAnchor {
  type: string;
  line: string;
  top: number;
}

// Choose the changed line that should stay put when the diff re-renders with
// different surrounding context. Prefer a change row that is on screen. When
// none is, the visible content is either above every change (nothing to
// correct: context is inserted below what the reader sees) or below some of
// them, in which case the nearest change above the viewport is pinned so the
// content after it stays where it is.
export function pickChangeAnchor(
  rows: readonly ChangeRowGeometry[],
  viewportTop: number,
  viewportBottom: number,
): ChangeLineAnchor | null {
  let visible: ChangeRowGeometry | null = null;
  let above: ChangeRowGeometry | null = null;
  for (const row of rows) {
    if (row.bottom >= viewportTop && row.top <= viewportBottom) {
      if (!visible || row.top < visible.top) visible = row;
    } else if (row.bottom < viewportTop && (!above || row.bottom > above.bottom)) {
      above = row;
    }
  }
  const chosen = visible ?? above;
  return chosen ? { type: chosen.type, line: chosen.line, top: chosen.top } : null;
}
