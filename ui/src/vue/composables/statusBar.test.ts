import assert from "node:assert/strict";
import { test, type TestContext } from "node:test";
import { effectScope, nextTick, ref } from "vue";
import { api, type StatusBar } from "../../services/api";
import { RETURN_DEBOUNCE_MS, useStatusBar } from "./statusBar";

const fakeDocument = Object.assign(new EventTarget(), { visibilityState: "visible" });
const fakeWindow = new EventTarget();
Object.assign(globalThis, { document: fakeDocument, window: fakeWindow });

function setVisibility(state: "visible" | "hidden") {
  fakeDocument.visibilityState = state;
  fakeDocument.dispatchEvent(new Event("visibilitychange"));
}

// Coming back to the tab: visibilitychange, then focus.
function comeBack(t: TestContext) {
  setVisibility("visible");
  fakeWindow.dispatchEvent(new Event("focus"));
  t.mock.timers.tick(RETURN_DEBOUNCE_MS);
}

function focus(t: TestContext) {
  fakeWindow.dispatchEvent(new Event("focus"));
  t.mock.timers.tick(RETURN_DEBOUNCE_MS);
}

// Settle pending promise callbacks and Vue's watcher flush.
async function settle() {
  for (let i = 0; i < 5; i++) await nextTick();
}

function harness(t: TestContext) {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  fakeDocument.visibilityState = "visible";
  const requests: { id: string; signal: AbortSignal; resolve: (s: StatusBar) => void }[] = [];
  const offline = ref(false);
  t.mock.method(
    api,
    "getStatusBar",
    (id: string, signal: AbortSignal) =>
      new Promise<StatusBar>((resolve, reject) => {
        if (offline.value) return reject(new Error("offline"));
        requests.push({ id, signal, resolve });
        signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
      }),
  );
  const id = ref<string | null>("c1");
  const ready = ref(true);
  const seq = ref(1);
  const scope = effectScope();
  const { status, stale } = scope.run(() =>
    useStatusBar(
      () => id.value,
      () => ready.value,
      [seq],
    ),
  )!;
  t.after(() => scope.stop());
  return { requests, id, ready, offline, seq, status, stale, scope };
}

test("asks on open and again after each change", async (t) => {
  const { requests, seq, status } = harness(t);
  assert.deepEqual(
    requests.map((r) => r.id),
    ["c1"],
  );
  requests[0].resolve({ items: [{ body: "main", bgcolor: "red" }] });
  await settle();
  assert.deepEqual(status.value, { items: [{ body: "main", bgcolor: "red" }] });

  seq.value++;
  await settle();
  assert.equal(requests.length, 2);
  requests[1].resolve({ items: [{ body: "feature" }] });
  await settle();
  assert.deepEqual(status.value, { items: [{ body: "feature" }] });
});

test("changes during a request collapse into one more", async (t) => {
  const { requests, seq, status } = harness(t);
  seq.value++;
  await settle();
  seq.value++;
  await settle();
  assert.equal(requests.length, 1);
  requests[0].resolve({ items: [{ body: "old" }] });
  await settle();
  assert.equal(requests.length, 2);
  requests[1].resolve({ items: [{ body: "new" }] });
  await settle();
  assert.equal(requests.length, 2);
  assert.deepEqual(status.value, { items: [{ body: "new" }] });
});

test("switching conversations drops the old one's status and request", async (t) => {
  const { requests, id, status, stale } = harness(t);
  requests[0].resolve({ items: [{ body: "c1 status" }] });
  await settle();
  assert.equal(stale.value, false);
  id.value = "c2";
  await settle();
  // Kept only to hold the row's place until c2's comes.
  assert.deepEqual(status.value, { items: [{ body: "c1 status" }] });
  assert.equal(stale.value, true);
  assert.deepEqual(
    requests.map((r) => r.id),
    ["c1", "c2"],
  );

  // Leaving c2 before its hook finishes cancels it, so c3 needn't wait.
  id.value = "c3";
  await settle();
  assert.equal(requests[1].signal.aborted, true);
  assert.deepEqual(
    requests.map((r) => r.id),
    ["c1", "c2", "c3"],
  );
  requests[1].resolve({ items: [{ body: "c2 status" }] });
  await settle();
  assert.deepEqual(status.value, { items: [{ body: "c1 status" }] });
  requests[2].resolve({ items: [{ body: "c3 status" }] });
  await settle();
  assert.deepEqual(status.value, { items: [{ body: "c3 status" }] });
  assert.equal(stale.value, false);
});

test("a loading conversation waits, then asks once", async (t) => {
  const { requests, id, ready, seq, status, stale } = harness(t);
  requests[0].resolve({ items: [{ body: "c1 status" }] });
  await settle();
  // Switching marks it loading in the same tick; its messages come in.
  id.value = "c2";
  ready.value = false;
  await settle();
  seq.value++;
  await settle();
  assert.equal(requests.length, 1);
  assert.equal(stale.value, true);

  ready.value = true;
  await settle();
  assert.deepEqual(
    requests.map((r) => r.id),
    ["c1", "c2"],
  );
  requests[1].resolve({});
  await settle();
  assert.deepEqual(status.value, {});
  assert.equal(stale.value, false);
  assert.equal(requests.length, 2);
});

test("a failure for a new conversation drops the old one's status", async (t) => {
  const { requests, id, offline, status, stale } = harness(t);
  t.mock.method(console, "warn", () => {});
  requests[0].resolve({ items: [{ body: "c1 status" }] });
  await settle();
  offline.value = true;
  id.value = "c2";
  await settle();
  assert.equal(status.value, null);
  assert.equal(stale.value, false);
});

test("disposing cancels the request and anything queued behind it", async (t) => {
  const { requests, seq, scope } = harness(t);
  seq.value++;
  await settle();
  scope.stop();
  await settle();
  assert.equal(requests[0].signal.aborted, true);
  assert.equal(requests.length, 1);
});

test("a hidden tab waits until it is visible again", async (t) => {
  const { requests, seq, scope } = harness(t);
  requests[0].resolve({});
  await settle();
  setVisibility("hidden");
  seq.value++;
  await settle();
  assert.equal(requests.length, 1);

  // Both events of coming back make one request.
  comeBack(t);
  await settle();
  assert.equal(requests.length, 2);
  requests[1].resolve({});
  await settle();
  assert.equal(requests.length, 2);

  // Coming back asks even with no change: the branch may have moved.
  setVisibility("hidden");
  comeBack(t);
  await settle();
  assert.equal(requests.length, 3);
  requests[2].resolve({});
  await settle();

  // As does focusing the window, which a visible tab gets back from a
  // terminal beside it.
  focus(t);
  await settle();
  assert.equal(requests.length, 4);
  requests[3].resolve({});
  await settle();

  scope.stop();
  setVisibility("hidden");
  comeBack(t);
  await settle();
  assert.equal(requests.length, 4);
});

test("coming back during a request from before asks again once", async (t) => {
  const { requests, seq, status } = harness(t);
  // The hook is slow; meanwhile the user leaves, switches branch, and comes
  // back. The answer on its way is from before.
  setVisibility("hidden");
  comeBack(t);
  requests[0].resolve({ items: [{ body: "old-branch" }] });
  await settle();
  assert.equal(requests.length, 2);
  requests[1].resolve({ items: [{ body: "new-branch" }] });
  await settle();
  assert.equal(requests.length, 2);
  assert.deepEqual(status.value, { items: [{ body: "new-branch" }] });

  // Likewise a request a message started while the window was in the
  // background, still on its way when the user focuses it.
  seq.value++;
  await settle();
  assert.equal(requests.length, 3);
  focus(t);
  requests[2].resolve({ items: [{ body: "old-branch" }] });
  await settle();
  assert.equal(requests.length, 4);
  requests[3].resolve({ items: [{ body: "new-branch" }] });
  await settle();
  assert.deepEqual(status.value, { items: [{ body: "new-branch" }] });
});

test("disposing cancels a pending return", async (t) => {
  const { requests, scope } = harness(t);
  requests[0].resolve({});
  await settle();
  fakeWindow.dispatchEvent(new Event("focus"));
  scope.stop();
  t.mock.timers.tick(RETURN_DEBOUNCE_MS);
  await settle();
  assert.equal(requests.length, 1);
});

test("a null id asks nothing and shows nothing", async (t) => {
  const { requests, id, status, stale } = harness(t);
  requests[0].resolve({ items: [{ body: "main" }] });
  await settle();
  id.value = null;
  await settle();
  assert.equal(status.value, null);
  assert.equal(stale.value, false);
  assert.equal(requests.length, 1);
});
