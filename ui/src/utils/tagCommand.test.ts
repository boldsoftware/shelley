import assert from "node:assert/strict";
import { test } from "node:test";
import { diffTags, parseTagCommand } from "./tagCommand";

test("/tag sends edits in command order", () => {
  assert.deepEqual(parseTagCommand("+bug -wip -BUG"), [
    { tag: "bug", add: true },
    { tag: "wip", add: false },
    { tag: "BUG", add: false },
  ]);
});

test("tag drafts send only changed tags, ignoring capitalization", () => {
  assert.deepEqual(diffTags(["Infra", "WIP"], ["infra", "docs"]), [
    { tag: "WIP", add: false },
    { tag: "docs", add: true },
  ]);
});

test("/tag rejects unsigned edits", () => {
  assert.throws(() => parseTagCommand("+bug wip"), /Usage/);
  assert.throws(() => parseTagCommand("+"), /Usage/);
});
