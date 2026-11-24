import assert from "node:assert/strict";
import { test, type TestContext } from "node:test";
import { useDraftAutosave } from "./draftAutosave";

// Outside a component, onUnmounted only warns.
console.warn = () => {};

function harness(t: TestContext) {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const saves: { value: string; done: () => void }[] = [];
  const autosave = useDraftAutosave(
    (value) => new Promise<void>((done) => saves.push({ value, done })),
    { baseDelayMs: 10 },
  );
  return { autosave, saves, tick: () => t.mock.timers.tick(10) };
}

async function settle() {
  for (let i = 0; i < 5; i++) await Promise.resolve();
}

test("reset hands back the value waiting behind a save in flight", async (t) => {
  const { autosave, saves, tick } = harness(t);
  autosave.schedule("a");
  tick();
  assert.deepEqual(
    saves.map((s) => s.value),
    ["a"],
  );
  autosave.schedule("ab");
  assert.equal(autosave.reset(), "ab");
  assert.equal(autosave.pending(), false);
  saves[0].done();
  await settle();
  tick();
  assert.equal(saves.length, 1, "nothing saved for the next composer");
});

test("reset hands back nothing when the save in flight is the latest", async (t) => {
  const { autosave, tick } = harness(t);
  autosave.schedule("a");
  tick();
  assert.equal(autosave.reset(), null);
});

test("a save finishing after reset doesn't count as saved for the next composer", async (t) => {
  const { autosave, saves, tick } = harness(t);
  autosave.schedule("same");
  tick();
  autosave.reset();
  saves[0].done();
  await settle();
  autosave.schedule("same");
  tick();
  assert.deepEqual(
    saves.map((s) => s.value),
    ["same", "same"],
  );
});

test("observed text isn't saved again", async (t) => {
  const { autosave, saves, tick } = harness(t);
  autosave.observe("theirs");
  autosave.schedule("theirs");
  tick();
  assert.equal(saves.length, 0);
});

test("text saved last saves again once the server's copy is unknown", async (t) => {
  const { autosave, saves, tick } = harness(t);
  autosave.schedule("continue");
  tick();
  saves[0].done();
  await settle();
  autosave.observe(null);
  autosave.schedule("continue");
  tick();
  assert.deepEqual(
    saves.map((s) => s.value),
    ["continue", "continue"],
  );
});

test("text observed while a save is in flight stays what the server has", async (t) => {
  const { autosave, saves, tick } = harness(t);
  autosave.schedule("mine");
  tick();
  autosave.observe("theirs");
  saves[0].done();
  await settle();
  autosave.schedule("theirs");
  tick();
  autosave.schedule("mine");
  tick();
  assert.deepEqual(
    saves.map((s) => s.value),
    ["mine", "mine"],
  );
});

test("reset hands back a revert to the saved text while other text is in flight", async (t) => {
  const { autosave, saves, tick } = harness(t);
  autosave.schedule("saved");
  tick();
  saves[0].done();
  await settle();
  autosave.schedule("other");
  tick();
  autosave.schedule("saved");
  assert.equal(autosave.reset(), "saved");
});

test("reset hands back the next composer's text, though the last one's save in flight is the same", async (t) => {
  const { autosave, tick } = harness(t);
  autosave.schedule("same");
  tick();
  autosave.reset();
  autosave.schedule("same");
  assert.equal(autosave.reset(), "same");
});
