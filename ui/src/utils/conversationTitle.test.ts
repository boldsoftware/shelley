import assert from "node:assert/strict";
import { test } from "node:test";
import { humanizeSlug } from "./conversationTitle";

test("replaces hyphens with spaces and capitalizes only the first letter", () => {
  assert.equal(
    humanizeSlug("remove-the-required-general-category"),
    "Remove the required general category",
  );
  assert.equal(humanizeSlug("fix-ui-in-cp-2"), "Fix ui in cp 2");
  assert.equal(humanizeSlug(""), "");
});
