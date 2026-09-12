import assert from "node:assert/strict";
import { test } from "node:test";
import { api } from "./api";

test("findFiles requests folders only when explicitly enabled, preserving cancellation", async (t) => {
  const requests: { url: URL; signal?: AbortSignal | null }[] = [];
  t.mock.method(globalThis, "fetch", async (input: string, init?: RequestInit) => {
    requests.push({ url: new URL(input, "http://localhost"), signal: init?.signal });
    return Response.json({ matches: [{ path: "My Folder/", is_dir: true }] });
  });
  const controller = new AbortController();
  const response = await api.findFiles("/work space", "My Folder", controller.signal, {
    content: "skip",
    includeDirs: true,
  });
  assert.equal(requests[0].url.searchParams.get("include_dirs"), "true");
  assert.equal(requests[0].url.searchParams.get("dir"), "/work space");
  assert.equal(requests[0].url.searchParams.get("q"), "My Folder");
  assert.equal(requests[0].url.searchParams.get("content"), "skip");
  assert.equal(requests[0].signal, controller.signal);
  assert.equal(response.matches[0].is_dir, true);
  await api.findFiles("/work", "file");
  await api.findFiles("/work", "file", undefined, { includeDirs: false });
  assert.equal(requests[1].url.searchParams.has("include_dirs"), false);
  assert.equal(requests[2].url.searchParams.has("include_dirs"), false);
});
