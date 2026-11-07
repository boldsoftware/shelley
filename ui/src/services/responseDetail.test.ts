import assert from "node:assert/strict";
import { test } from "node:test";
import { apiJSON } from "./api";
import { bodyDetail } from "./responseDetail";

const proxyPage = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Service unavailable</title>
<style>*, *::before { margin: 0; }</style></head>
<body><h1>Service unavailable</h1></body></html>`;

test("an HTML error page becomes the response's status, logged rather than shown", async (t) => {
  const warn = t.mock.method(console, "warn", () => {});
  t.mock.method(
    globalThis,
    "fetch",
    async () =>
      new Response(proxyPage, {
        status: 503,
        statusText: "Service Unavailable",
        headers: { "Content-Type": "text/html; charset=utf-8" },
      }),
  );
  await assert.rejects(apiJSON("/api/customizations", "Failed to load build status"), {
    message: "Failed to load build status: HTTP 503 Service Unavailable",
    status: 503,
  });
  assert.equal(warn.mock.callCount(), 1);
  assert.equal(warn.mock.calls[0].arguments[1], proxyPage);
});

test("bodyDetail keeps the server's short errors and drops markup", () => {
  assert.equal(bodyDetail("hook failed: exit 1\n", "text/plain"), "hook failed: exit 1");
  assert.equal(bodyDetail('{"error":"no such task"}', "application/json"), "no such task");
  assert.equal(bodyDetail('{"message":"too big"}', ""), "too big");
  assert.equal(bodyDetail('{"code":7}', "application/json"), "");
  assert.equal(bodyDetail(proxyPage, ""), "");
  assert.equal(bodyDetail(proxyPage, "text/plain"), "");
  assert.equal(bodyDetail("<p>oops</p>", "text/html"), "");
  assert.equal(bodyDetail("", "text/plain"), "");
  const long = bodyDetail("x".repeat(1000), "text/plain");
  assert.equal(long.length, 300);
  assert.ok(long.endsWith("…"));
});
