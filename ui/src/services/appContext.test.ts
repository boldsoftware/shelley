import assert from "node:assert/strict";
import { test } from "node:test";
import { restoreAppContext, setAppContext, takeAppContext } from "./appContext";

const texts = (conversation: string) =>
  [...takeAppContext(conversation).values()].map((c) => c.context.text);

test("a view's update replaces its last; a send takes them all", () => {
  setAppContext("c", "a", { source: "A", text: "one" });
  setAppContext("c", "a", { source: "A", text: "two" });
  setAppContext("c", "b", { source: "B", text: "three" });
  setAppContext("other", "a", { source: "A", text: "elsewhere" });
  assert.deepEqual(texts("c"), ["two", "three"]);
  assert.deepEqual(texts("c"), []);
  assert.deepEqual(texts("other"), ["elsewhere"]);
});

test("clearing drops a view's context", () => {
  setAppContext("c", "a", { source: "A", text: "one" });
  setAppContext("c", "a", null);
  assert.deepEqual(texts("c"), []);
});

test("a failed send puts back what no view has replaced", () => {
  setAppContext("c", "a", { source: "A", text: "old a" });
  setAppContext("c", "b", { source: "B", text: "old b" });
  const taken = takeAppContext("c");
  setAppContext("c", "a", { source: "A", text: "new a" });
  restoreAppContext("c", taken);
  assert.deepEqual(texts("c").sort(), ["new a", "old b"]);
});

test("a failed send doesn't put back what its view cleared since", () => {
  setAppContext("c", "a", { source: "A", text: "old a" });
  const taken = takeAppContext("c");
  setAppContext("c", "a", null);
  restoreAppContext("c", taken);
  assert.deepEqual(texts("c"), []);
});

test("an older send's failure doesn't undo a newer one's take", () => {
  setAppContext("c", "a", { source: "A", text: "old a" });
  const older = takeAppContext("c");
  setAppContext("c", "a", { source: "A", text: "new a" });
  const newer = takeAppContext("c");
  restoreAppContext("c", older);
  assert.deepEqual(texts("c"), []);
  restoreAppContext("c", newer);
  assert.deepEqual(texts("c"), ["new a"]);
});
