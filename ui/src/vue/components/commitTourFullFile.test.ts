import { strict as assert } from "node:assert";
import {
  fileLineText,
  fullFileNames,
  patchFitsContents,
  pickChangeAnchor,
} from "./commitTourFullFile";

// fileLineText: 1-based lookup on the requested side; a trailing newline does
// not create a phantom empty last line (matching @pierre/diffs numbering).
const contents = { path: "b.txt", oldContent: "one\ntwo\nthree\n", newContent: "uno\ntwo\n" };
assert.equal(fileLineText(contents, "deletions", 1), "one");
assert.equal(fileLineText(contents, "deletions", 3), "three");
assert.equal(fileLineText(contents, "deletions", 4), null);
assert.equal(fileLineText(contents, "additions", 1), "uno");
assert.equal(fileLineText(contents, "additions", 2), "two");
assert.equal(fileLineText(contents, "additions", 3), null);
assert.equal(fileLineText(contents, "additions", 0), null);
// CRLF endings are preserved verbatim, as patchLineText does.
assert.equal(
  fileLineText({ path: "c", oldContent: "", newContent: "a\r\nb\r\n" }, "additions", 1),
  "a\r",
);
// An empty file has no lines at all.
assert.equal(fileLineText({ path: "c", oldContent: "", newContent: "" }, "deletions", 1), null);

// patchFitsContents: every hunk must lie within both sides of the file.
const hunk = { oldStart: 20, oldCount: 7, newStart: 20, newCount: 7 };
const forty = Array.from({ length: 40 }, (_, i) => `l${i}`).join("\n") + "\n";
assert.equal(patchFitsContents([hunk], { path: "f", oldContent: forty, newContent: forty }), true);
// The hunk may end exactly on the last line.
const twentySix = Array.from({ length: 26 }, (_, i) => `l${i}`).join("\n");
assert.equal(
  patchFitsContents([hunk], { path: "f", oldContent: twentySix, newContent: twentySix }),
  true,
);
// An unreadable side comes back empty and cannot hold the hunk.
assert.equal(patchFitsContents([hunk], { path: "f", oldContent: "", newContent: forty }), false);
assert.equal(patchFitsContents([hunk], { path: "f", oldContent: forty, newContent: "" }), false);
// A side that is too short by one line fails too.
const twentyFive = Array.from({ length: 25 }, (_, i) => `l${i}`).join("\n") + "\n";
assert.equal(
  patchFitsContents([hunk], { path: "f", oldContent: forty, newContent: twentyFive }),
  false,
);
// Every hunk counts, and a chunk with no hunks trivially fits.
assert.equal(
  patchFitsContents([hunk, { oldStart: 100, oldCount: 3, newStart: 100, newCount: 3 }], {
    path: "f",
    oldContent: forty,
    newContent: forty,
  }),
  false,
);
assert.equal(patchFitsContents([], { path: "f", oldContent: "", newContent: "" }), true);
// A zero-count side ("-0,0" / "+0,0") legitimately pairs with an empty file.
assert.equal(
  patchFitsContents([{ oldStart: 0, oldCount: 0, newStart: 1, newCount: 3 }], {
    path: "f",
    oldContent: "",
    newContent: "a\nb\nc\n",
  }),
  true,
);

// fullFileNames: falls back across the rename/new/deleted cases so both sides
// always get a name for language detection.
assert.deepEqual(
  fullFileNames({ oldPath: "a/old.go", newPath: "a/new.go", fileLabel: "a/new.go" }),
  { old: "a/old.go", new: "a/new.go" },
);
assert.deepEqual(fullFileNames({ oldPath: null, newPath: "x.ts", fileLabel: "x.ts" }), {
  old: "x.ts",
  new: "x.ts",
});
assert.deepEqual(fullFileNames({ oldPath: "x.ts", newPath: null, fileLabel: "x.ts" }), {
  old: "x.ts",
  new: "x.ts",
});
assert.deepEqual(fullFileNames({ oldPath: null, newPath: null, fileLabel: "File change" }), {
  old: "File change",
  new: "File change",
});

// pickChangeAnchor: prefer the topmost change row on screen.
const rows = [
  { type: "change-deletion", line: "29", top: -40, bottom: -22 },
  { type: "change-addition", line: "28", top: 10, bottom: 28 },
  { type: "change-addition", line: "199", top: 400, bottom: 418 },
];
assert.deepEqual(pickChangeAnchor(rows, 0, 600), { type: "change-addition", line: "28", top: 10 });
assert.deepEqual(pickChangeAnchor(rows, 300, 600), {
  type: "change-addition",
  line: "199",
  top: 400,
});
// A row straddling the top edge is still on screen.
assert.deepEqual(pickChangeAnchor(rows, -30, 0), { type: "change-deletion", line: "29", top: -40 });
// Nothing on screen but changes above: pin the nearest one above the fold.
assert.deepEqual(pickChangeAnchor(rows, 500, 900), {
  type: "change-addition",
  line: "199",
  top: 400,
});
assert.deepEqual(pickChangeAnchor(rows, 100, 300), {
  type: "change-addition",
  line: "28",
  top: 10,
});
// Every change is below the viewport: the reader is above all insertions.
assert.equal(pickChangeAnchor(rows, -500, -100), null);
assert.equal(pickChangeAnchor([], 0, 600), null);
// Split view lists the deletions column before the additions column; the
// choice must not depend on document order.
assert.deepEqual(
  pickChangeAnchor(
    [
      { type: "change-deletion", line: "50", top: 200, bottom: 218 },
      { type: "change-addition", line: "49", top: 20, bottom: 38 },
    ],
    0,
    600,
  ),
  { type: "change-addition", line: "49", top: 20 },
);

console.log("commitTourFullFile tests passed");
