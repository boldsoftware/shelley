import assert from "node:assert/strict";
import { test } from "node:test";
import { humanizeSlug, titleSlug } from "./conversationTitle";

test("replaces hyphens with spaces and capitalizes only the first letter", () => {
  assert.equal(
    humanizeSlug("remove-the-required-general-category"),
    "Remove the required general category",
  );
  assert.equal(humanizeSlug("fix-ui-in-cp-2"), "Fix ui in cp 2");
  assert.equal(humanizeSlug(""), "");
});

test("titleSlug turns a typed title back into a slug", () => {
  assert.equal(titleSlug("Fix header alignment"), "fix-header-alignment");
  assert.equal(titleSlug(humanizeSlug("fix-ui-in-cp-2")), "fix-ui-in-cp-2");
  assert.equal(titleSlug("  Ünïcode_and  spaces!! "), "ncode-and-spaces");
  assert.equal(titleSlug("!!!"), "");
  assert.equal(titleSlug("a".repeat(59) + " b"), "a".repeat(59));
});
