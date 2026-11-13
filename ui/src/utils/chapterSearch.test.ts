import assert from "node:assert/strict";
import { test } from "node:test";
import {
  chapterMatchScore,
  chapterSearchSegments,
  mergeChapterResults,
  scoreChapterIndex,
} from "./chapterSearch";

test("ranks exact chapter titles before prefixes, phrases, and scattered terms", () => {
  assert.equal(chapterMatchScore("API design", "API design"), 4);
  assert.equal(chapterMatchScore("API design choices", "api design"), 3);
  assert.equal(chapterMatchScore("Review API design choices", "api design"), 2);
  assert.equal(chapterMatchScore("API choices and design", "api design"), 1);
  assert.equal(chapterMatchScore("API only", "api design"), 0);
  for (const query of ["", " ", "?!"]) {
    assert.equal(chapterMatchScore("API design", query), 0);
  }
  assert.equal(chapterMatchScore("日本語 API: design", "日本語 api"), 3);
});

test("highlights literal Unicode words, preferring longer overlapping terms", () => {
  assert.deepEqual(chapterSearchSegments("<b>Cat catalog</b>", "CAT catalog [.*]"), [
    { text: "<b>", mark: false },
    { text: "Cat", mark: true },
    { text: " ", mark: false },
    { text: "catalog", mark: true },
    { text: "</b>", mark: false },
  ]);
  assert.deepEqual(chapterSearchSegments("日本語 API", "日本語"), [
    { text: "", mark: false },
    { text: "日本語", mark: true },
    { text: " API", mark: false },
  ]);
  assert.deepEqual(chapterSearchSegments("API", "?!"), [{ text: "API", mark: false }]);
});

test("scores only conversations with a matching chapter", () => {
  const chapter = (title: string) => ({ title, message_id: "message" });
  assert.deepEqual(
    scoreChapterIndex(
      {
        exact: [chapter("API"), chapter("unrelated")],
        prefix: [chapter("API design")],
        absent: [chapter("unrelated")],
        empty: [],
      },
      "api",
    ),
    new Map([
      ["exact", 4],
      ["prefix", 3],
    ]),
  );
});

test("merges in-scope chapter hits first without losing FTS metadata or stable ordering", () => {
  type Hit = { conversation_id: string; snippet?: string; is_draft?: boolean };
  const fts: Hit[] = [
    { conversation_id: "text-first", snippet: "a" },
    { conversation_id: "both", snippet: "b" },
    { conversation_id: "text-second", snippet: "c" },
  ];
  const pool: Hit[] = [
    { conversation_id: "both" },
    { conversation_id: "chapter-only" },
    { conversation_id: "same-score" },
    { conversation_id: "draft", is_draft: true },
    { conversation_id: "no-match" },
  ];
  const scores = new Map([
    ["both", 3],
    ["chapter-only", 4],
    ["same-score", 3],
    ["out-of-scope", 4],
    ["draft", 4],
  ]);
  const results = mergeChapterResults(fts, pool, scores);
  assert.deepEqual(
    results.map((c) => c.conversation_id),
    ["chapter-only", "both", "same-score", "text-first", "text-second"],
  );
  assert.equal(results[1], fts[1]);
  assert.equal(fts.length, 3);
  assert.deepEqual(mergeChapterResults(fts, pool, new Map()), fts);
});
