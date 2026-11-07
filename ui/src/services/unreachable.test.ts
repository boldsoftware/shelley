import assert from "node:assert/strict";
import { test } from "node:test";
import { ApiError } from "./api";
import { isUnreachable, UNREACHABLE_ERROR_MS, unreachableLine } from "./unreachable";

test("a proxy's 502-504 and no response are Shelley unreachable; other failures aren't", () => {
  for (const status of [0, 502, 503, 504]) assert.ok(isUnreachable(new ApiError("x", status)));
  for (const status of [400, 404, 500]) assert.ok(!isUnreachable(new ApiError("x", status)));
  assert.ok(!isUnreachable(new Error("x")));
});

test("unreachable is a quiet restarting or reconnecting line, an error only after long", () => {
  assert.deepEqual(unreachableLine(0, 1000, true), { text: "Restarting…", error: false });
  assert.deepEqual(unreachableLine(0, UNREACHABLE_ERROR_MS - 1, false), {
    text: "Reconnecting…",
    error: false,
  });
  assert.deepEqual(unreachableLine(0, 3 * 60_000 + 5000, true), {
    text: "Can't reach Shelley for 3 min; retrying",
    error: true,
  });
});
