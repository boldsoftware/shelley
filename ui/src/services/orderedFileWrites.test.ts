import assert from "node:assert/strict";
import { test } from "node:test";
import { unsavedFileContent, writeFileInOrder } from "./orderedFileWrites";

test("an older successful write cannot discard the latest failed buffer", async () => {
  const fetchBefore = globalThis.fetch;
  let allowFirst!: () => void;
  let firstStarted!: () => void;
  const firstAllowed = new Promise<void>((resolve) => (allowFirst = resolve));
  const started = new Promise<void>((resolve) => (firstStarted = resolve));
  let requests = 0;
  globalThis.fetch = async () => {
    const request = ++requests;
    if (request === 1) {
      firstStarted();
      await firstAllowed;
    }
    return new Response(null, { status: request === 3 ? 503 : 200 });
  };

  const path = "/tmp/ordered-file-writes-test.md";
  try {
    const first = writeFileInOrder(path, "same text");
    await started;
    const second = writeFileInOrder(path, "intermediate text");
    const latest = writeFileInOrder(path, "same text");
    allowFirst();
    assert.deepEqual(
      (await Promise.all([first, second, latest])).map((response) => response.status),
      [200, 200, 503],
    );
    assert.equal(unsavedFileContent(path), "same text");
  } finally {
    allowFirst();
    globalThis.fetch = fetchBefore;
  }
});
