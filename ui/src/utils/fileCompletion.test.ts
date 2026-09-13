import assert from "node:assert/strict";
import { test } from "node:test";
import { fileTokenAt, insertFilePath } from "./fileCompletion";

test("@ starts a query at a token boundary, including empty and multiline queries", () => {
  assert.deepEqual(fileTokenAt("@", 1), { start: 0, end: 1, query: "" });
  assert.deepEqual(fileTokenAt("Read\n@docs/file", 15), { start: 5, end: 15, query: "docs/file" });
  assert.equal(fileTokenAt("email user@example.com", 22), null);
  assert.equal(fileTokenAt("\\@file", 6), null);
  assert.equal(fileTokenAt("@file ", 6), null);
  assert.equal(fileTokenAt("@file", 0), null);
  assert.equal(fileTokenAt("@file", 2, 4), null);
});

test("only query text before the cursor is searched; the entire token is replaced", () => {
  const text = "Compare @README-old with @other";
  const token = fileTokenAt(text, 12)!;
  assert.deepEqual(token, { start: 8, end: 19, query: "REA" });
  const replacement = insertFilePath(text, token, "/work", "README.md");
  assert.equal(replacement.text, 'Compare "/work/README.md" with @other');
  assert.equal(replacement.text.slice(replacement.cursor), " with @other");
});

test("quoted queries handle spaces, closing quotes, escaped quotes and backslashes", () => {
  for (const text of ['@"My Notes', '@"My Notes"']) {
    assert.equal(fileTokenAt(text, text.length)?.query, "My Notes");
  }
  const query = `@${JSON.stringify('My "Notes"\\')}`;
  assert.equal(fileTokenAt(query, query.length)?.query, 'My "Notes"\\');
  assert.equal(fileTokenAt(query, 1), null);
  const middle = '@"My Notes" then continue';
  const token = fileTokenAt(middle, 5)!;
  assert.deepEqual(token, { start: 0, end: 11, query: "My " });
  assert.equal(
    insertFilePath(middle, token, "/notes", "My Notes.md").text,
    '"/notes/My Notes.md" then continue',
  );
});

test("inserted paths are always quoted, escaped and rooted at search_dir", () => {
  const token = fileTokenAt("@", 1)!;
  for (const path of [
    "file.txt",
    "My Notes.md",
    'quote".md',
    "back\\slash",
    "line\nbreak.md",
    "日本語.md",
  ]) {
    const result = insertFilePath("@", token, "/elsewhere/", path);
    assert.equal(JSON.parse(result.text.trimEnd()), `/elsewhere/${path}`);
    assert.equal(result.cursor, result.text.length);
    assert.ok(result.text.endsWith(" "));
  }
  assert.equal(insertFilePath("@", token, "/", "root.txt").text, '"/root.txt" ');
});

test("completion preserves trailing prose punctuation; quotes allow it in filenames", () => {
  for (const punctuation of [",", ":", ";", "!", "?", "(", ")", "[", "]", "{", "}"]) {
    const text = `Compare @READ${punctuation} please`;
    const token = fileTokenAt(text, 13)!;
    assert.equal(token.end, 13);
    assert.equal(
      insertFilePath(text, token, "/work", "README.md").text,
      `Compare "/work/README.md"${punctuation} please`,
    );
    assert.equal(fileTokenAt(text, 14), null);
  }
  const query = '@"file (copy), final:notes.md"';
  assert.equal(fileTokenAt(query, query.length)?.query, "file (copy), final:notes.md");
});
