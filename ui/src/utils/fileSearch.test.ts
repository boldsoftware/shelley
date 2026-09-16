import assert from "node:assert/strict";
import { test } from "node:test";
import { highlightSegments, mergeContentMatches } from "./fileSearch";

test("highlightSegments coalesces Unicode code-point offsets", () => {
  assert.deepEqual(highlightSegments("x🙂needle", [2, 3, 4, 5, 6, 7]), [
    { text: "x🙂", hit: false },
    { text: "needle", hit: true },
  ]);
  assert.deepEqual(highlightSegments("plain"), [{ text: "plain", hit: false }]);
});

test("mergeContentMatches annotates existing rows and caps appended hits", () => {
  const merged = mergeContentMatches(
    [{ path: "named.ts" }],
    [
      { path: "named.ts", line: 2, snippet: "needle" },
      { path: "content.ts", line: 3, snippet: "needle" },
      { path: "dropped.ts", line: 4, snippet: "needle" },
    ],
    2,
  );
  assert.deepEqual(merged, {
    matches: [
      { path: "named.ts", line: 2, snippet: "needle", snippet_matched_indexes: undefined },
      { path: "content.ts", line: 3, snippet: "needle" },
    ],
    capped: true,
  });
});
