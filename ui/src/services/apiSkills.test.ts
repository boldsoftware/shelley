import assert from "node:assert/strict";
import { test } from "node:test";
import { api } from "./api";

test("skills uses the API prefix, encoded cwd and optional conversation, and passes cancellation", async (t) => {
  const requests: { url: URL; signal?: AbortSignal | null }[] = [];
  const skills = [
    {
      name: "foo",
      description: "Foo",
      activate: "custom load foo",
      origin: "File",
      source_path: "/work/foo.md",
    },
  ];
  t.mock.method(globalThis, "fetch", async (input: string, init?: RequestInit) => {
    requests.push({ url: new URL(input, "http://localhost"), signal: init?.signal });
    return Response.json({ skills });
  });
  const controller = new AbortController();
  assert.deepEqual(await api.getSkills("/work space", null, controller.signal), { skills });
  assert.equal(requests[0].url.pathname, "/api/skills");
  assert.equal(requests[0].url.searchParams.get("cwd"), "/work space");
  assert.equal(requests[0].url.searchParams.has("conversation_id"), false);
  assert.equal(requests[0].signal, controller.signal);
  await api.getSkills("/work", "conversation-one");
  assert.equal(requests[1].url.searchParams.get("conversation_id"), "conversation-one");
});

test("skills propagates catalog errors with their server detail", async (t) => {
  t.mock.method(
    globalThis,
    "fetch",
    async () => new Response("Catalog unavailable", { status: 503 }),
  );
  await assert.rejects(api.getSkills("/work", null), /Failed to load skills: Catalog unavailable/);
});
