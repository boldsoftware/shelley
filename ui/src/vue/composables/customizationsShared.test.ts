import assert from "node:assert/strict";
import { test } from "node:test";
import { effectScope } from "vue";

// useCustomizations reads workspace mode and the page's visibility; stand in
// for both before the module loads.
const listeners = new Set<string>();
const target = {
  addEventListener: (type: string) => listeners.add(type),
  removeEventListener: (type: string) => listeners.delete(type),
};
Object.assign(globalThis, {
  window: { ...target, __SHELLEY_INIT__: { workspace: true } },
  document: { ...target, hidden: false },
});
const { customizationsApi } = await import("../../services/customizations");
const { useCustomizations } = await import("./customizations");

test("components share one poll; the last to unmount stops it", async (t) => {
  const list = t.mock.method(customizationsApi, "list", async () => []);
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const drawer = effectScope();
  const header = effectScope();
  const first = drawer.run(useCustomizations)!;
  const second = header.run(useCustomizations)!;
  assert.equal(first, second);
  assert.equal(list.mock.callCount(), 1);
  assert.deepEqual([...listeners].sort(), ["focus", "visibilitychange"]);

  drawer.stop();
  assert.equal(listeners.size, 2);
  await first.refresh();
  t.mock.timers.tick(5000);
  assert.equal(list.mock.callCount(), 2);

  header.stop();
  assert.equal(listeners.size, 0);
  await Promise.resolve();
  t.mock.timers.tick(5000);
  assert.equal(list.mock.callCount(), 2);

  // A later user starts a new poll.
  const next = effectScope();
  assert.notEqual(next.run(useCustomizations), first);
  assert.equal(list.mock.callCount(), 3);
  next.stop();
});
