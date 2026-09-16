import assert from "node:assert/strict";
import { test } from "node:test";
import { fileTokenAt, insertFilePath, relativeFileMatch, relativeFilePath } from "./fileCompletion";

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
  const replacement = insertFilePath(text, token, "README.md");
  assert.equal(replacement.text, "Compare @README.md with @other");
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
  assert.equal(insertFilePath(middle, token, "My Notes.md").text, '@"My Notes.md" then continue');
});

test("inserted references stay relative and are quoted only when required", () => {
  const token = fileTokenAt("@", 1)!;
  assert.equal(insertFilePath("@", token, "file.txt").text, "@file.txt ");
  assert.equal(insertFilePath("@", token, "src/file.ts").text, "@src/file.ts ");
  assert.equal(insertFilePath("@", token, "日本語.md").text, "@日本語.md ");
  for (const path of ["My Notes.md", 'quote".md', "file,name.md", "line\nbreak.md"]) {
    const result = insertFilePath("@", token, path);
    assert.equal(result.text, `@${JSON.stringify(path)} `);
    assert.equal(result.cursor, result.text.length);
  }
});

test("search results are shown relative to the conversation cwd", () => {
  assert.equal(relativeFilePath("/work", "/work", "src/main.ts"), "src/main.ts");
  assert.equal(relativeFilePath("/work", "/work/docs", "guide.md"), "docs/guide.md");
  assert.equal(relativeFilePath("/work/project", "/work/shared", "notes.md"), "../shared/notes.md");
  assert.equal(relativeFilePath("/work", "/work", "docs/"), "docs/");
  assert.equal(relativeFilePath("/work", "/work", "weird\\name.txt"), "weird\\name.txt");
  assert.deepEqual(
    relativeFileMatch("/work", "/work/docs", { path: "guide.md", matched_indexes: [0, 1] }),
    { path: "docs/guide.md", matched_indexes: [5, 6] },
  );
  assert.deepEqual(
    relativeFileMatch("/work/project", "/work", {
      path: "project/README.md",
      matched_indexes: [8, 9, 10],
    }),
    { path: "README.md", matched_indexes: [0, 1, 2] },
  );
});

test("completion preserves trailing prose punctuation; quotes allow it in filenames", () => {
  for (const punctuation of [",", ":", ";", "!", "?", "(", ")", "[", "]", "{", "}"]) {
    const text = `Compare @READ${punctuation} please`;
    const token = fileTokenAt(text, 13)!;
    assert.equal(token.end, 13);
    assert.equal(
      insertFilePath(text, token, "README.md").text,
      `Compare @README.md${punctuation} please`,
    );
    assert.equal(fileTokenAt(text, 14), null);
  }
  const query = '@"file (copy), final:notes.md"';
  assert.equal(fileTokenAt(query, query.length)?.query, "file (copy), final:notes.md");
});
