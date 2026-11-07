import { layoutPanes, PANE_HEIGHT, parseStoredPanes, serializePanes } from "./usagePanes";

let failed = 0;
function assert(cond: boolean, msg: string) {
  if (!cond) {
    failed++;
    console.error(`FAIL: ${msg}`);
  }
}

assert(
  PANE_HEIGHT.context === PANE_HEIGHT.cumulative,
  "context and cumulative cost get the same height",
);
assert(
  PANE_HEIGHT.incremental * 3 === PANE_HEIGHT.cumulative,
  "incremental cost gets a third of the height",
);

const all = layoutPanes(["incremental", "context", "cumulative"], 6, 10, 1);
assert(
  all.map((b) => b.pane).join() === "context,cumulative,incremental",
  `stack order is fixed: ${all.map((b) => b.pane)}`,
);
assert(all[0].top === 6, "first pane starts at the top");
assert(all[1].top === all[0].bottom + 10, "panes are separated by the gap");
assert(all[2].top === all[1].bottom + 10, "panes are separated by the gap");
assert(
  all.every((b) => b.bottom - b.top === b.height),
  "bottom = top + height",
);

const some = layoutPanes(["incremental", "context"], 6, 10, 1);
assert(some.length === 2 && some[1].top === some[0].bottom + 10, "hidden panes take no room");
assert(layoutPanes([], 6, 10, 1).length === 0, "no panes, no boxes");

// Remembering the choice across page loads.
const everything = "context,cumulative,incremental";
assert(parseStoredPanes(null).join() === everything, "nothing stored: every graph");
assert(parseStoredPanes("not json").join() === everything, "bad JSON: every graph");
assert(parseStoredPanes('{"context":true}').join() === everything, "wrong shape: every graph");
assert(parseStoredPanes("[]").length === 0, "all switched off is a valid choice");
assert(
  parseStoredPanes('["incremental","context"]').join() === "context,incremental",
  "stored panes come back in stack order",
);
assert(
  parseStoredPanes('["cumulative","bogus",7]').join() === "cumulative",
  "unknown names dropped",
);
assert(serializePanes(["incremental", "context"]) === '["context","incremental"]', "stack order");
assert(parseStoredPanes(serializePanes(["cumulative"])).join() === "cumulative", "round trip");

if (failed) process.exit(1);
console.log("✓ usage pane layout");
