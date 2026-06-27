// draftCache tests — localStorage mirror of the composer autosave.
//
// Run via `pnpm test` (see scripts/run-tests.mjs).

import { loadCachedDraft, saveCachedDraft, clearCachedDraft, pickDraft } from "./draftCache";

function assert(cond: boolean, msg: string): void {
  if (!cond) throw new Error(`Assertion failed: ${msg}`);
}
function run(name: string, fn: () => void): void {
  try {
    fn();
    console.log(`\u2713 ${name}`);
  } catch (err) {
    console.error(`\u2717 ${name}`);
    throw err;
  }
}

// Minimal in-memory localStorage polyfill for Node.
function installLocalStorage(): void {
  const store = new Map<string, string>();
  const ls = {
    getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
    setItem: (k: string, v: string) => void store.set(k, String(v)),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
    key: (i: number) => Array.from(store.keys())[i] ?? null,
    get length() {
      return store.size;
    },
  };
  Object.defineProperty(globalThis, "localStorage", { value: ls, configurable: true });
}

installLocalStorage();

run("round-trips a cached draft by id", () => {
  saveCachedDraft("c123", "hello", "2026-01-01T00:00:05Z");
  const got = loadCachedDraft("c123");
  assert(got?.value === "hello" && got?.basedOn === "2026-01-01T00:00:05Z", "loads what was saved");
});

run("uses a distinct slot for the new-conversation session", () => {
  saveCachedDraft(null, "new draft", "");
  saveCachedDraft("c1", "existing", "2026-01-01T00:00:09Z");
  assert(loadCachedDraft(null)?.value === "new draft", "null slot isolated");
  assert(loadCachedDraft("c1")?.value === "existing", "id slot isolated");
});

run("returns null for an absent or malformed entry", () => {
  assert(loadCachedDraft("missing") === null, "absent → null");
  localStorage.setItem("shelley-draft:bad", "{not json");
  assert(loadCachedDraft("bad") === null, "malformed → null");
});

run("clearCachedDraft removes the entry", () => {
  saveCachedDraft("c9", "x", "");
  clearCachedDraft("c9");
  assert(loadCachedDraft("c9") === null, "cleared → null");
});

run("pickDraft keeps local edits the server never acknowledged", () => {
  // Connection dropped: server's updated_at is frozen at t5; the user kept
  // typing, so the cache was stamped with that same t5 but holds newer text.
  const server = { value: "saved at t5", updatedAt: "2026-01-01T00:00:05Z" };
  const local = { value: "typed after t5", basedOn: "2026-01-01T00:00:05Z" };
  assert(pickDraft(server, local).value === "typed after t5", "unacked local wins");
});

run("pickDraft defers to a server copy that advanced past the cache", () => {
  // Another tab saved at t9; our cache predates it (based on t5).
  const server = { value: "newer from other tab", updatedAt: "2026-01-01T00:00:09Z" };
  const local = { value: "my stale text", basedOn: "2026-01-01T00:00:05Z" };
  assert(pickDraft(server, local).value === "newer from other tab", "newer server wins");
});

run("pickDraft defers to the server when text matches", () => {
  const server = { value: "same", updatedAt: "2026-01-01T00:00:05Z" };
  const local = { value: "same", basedOn: "2026-01-01T00:00:05Z" };
  assert(pickDraft(server, local).value === "same", "equal text → server (no-op)");
});

run("pickDraft defers to the server when there is no cache", () => {
  const server = { value: "server", updatedAt: "2026-01-01T00:00:05Z" };
  assert(pickDraft(server, null).value === "server", "no local → server");
});

run("pickDraft keeps a brand-new-view local draft (empty basedOn)", () => {
  // New-conversation view: no server row yet, so updatedAt is "" and the
  // local entry's basedOn is ""; the local text must survive a reload.
  const server = { value: "", updatedAt: "" };
  const local = { value: "composing something", basedOn: "" };
  assert(pickDraft(server, local).value === "composing something", "new-view local wins");
});

console.log("draftCache: all tests passed");
