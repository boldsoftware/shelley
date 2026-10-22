import assert from "node:assert/strict";
import { test } from "node:test";
import { api } from "./api";

test("getVersion works without AbortSignal.timeout and aborts a stalled request", async (t) => {
  t.mock.method(AbortSignal, "timeout", () => {
    throw new TypeError("AbortSignal.timeout is not a function");
  });
  t.mock.timers.enable({ apis: ["setTimeout"] });
  let signal: AbortSignal | undefined;
  t.mock.method(globalThis, "fetch", (_input: string, init?: RequestInit) => {
    signal = init?.signal ?? undefined;
    return new Promise((_resolve, reject) => {
      signal?.addEventListener("abort", () => reject(signal?.reason));
    });
  });

  const version = api.getVersion();
  assert.equal(signal?.aborted, false);
  t.mock.timers.tick(5000);
  await assert.rejects(version, { name: "AbortError" });
});

test("getVersion returns the tag", async (t) => {
  t.mock.method(globalThis, "fetch", async () => Response.json({ tag: "v0.2.0" }));
  assert.deepEqual(await api.getVersion(), { tag: "v0.2.0" });
});
