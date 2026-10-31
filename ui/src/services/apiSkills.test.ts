import assert from "node:assert/strict";
import { test } from "node:test";
import { api, type McpTool } from "./api";

const search: McpTool = {
  name: "search",
  description: "Search the web",
  inputSchema: {
    properties: { query: { type: "string", description: "Search terms" } },
    required: ["query"],
  },
};

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
    return Response.json(input.startsWith("/api/skills?") ? { skills } : []);
  });
  const controller = new AbortController();
  assert.deepEqual(await api.getSkills("/work space", null, controller.signal), {
    skills,
    warnings: [],
  });
  assert.equal(requests[0].url.pathname, "/api/skills");
  assert.equal(requests[0].url.searchParams.get("cwd"), "/work space");
  assert.equal(requests[0].url.searchParams.has("conversation_id"), false);
  assert.equal(requests[0].signal, controller.signal);
  assert.equal(requests[1].url.pathname, "/api/mcp/servers");
  assert.equal(requests[1].signal, controller.signal);
  await api.getSkills("/work", "conversation-one");
  assert.equal(requests[2].url.searchParams.get("conversation_id"), "conversation-one");
});

test("skills propagates catalog errors with their server detail", async (t) => {
  t.mock.method(
    globalThis,
    "fetch",
    async () => new Response("Catalog unavailable", { status: 503 }),
  );
  await assert.rejects(api.getSkills("/work", null), /Failed to load skills: Catalog unavailable/);
});

test("skills discovers actual tools with namespaced identities, display names, schemas and cancellation", async (t) => {
  const controller = new AbortController();
  const requests: { url: string; signal?: AbortSignal | null }[] = [];
  t.mock.method(globalThis, "fetch", async (input: string, init?: RequestInit) => {
    requests.push({ url: input, signal: init?.signal });
    if (input.startsWith("/api/skills?")) return Response.json({ skills: [] });
    if (input === "/api/mcp/servers") return Response.json([{ name: "web tools" }]);
    assert.equal(input, "/api/mcp/servers/web%20tools/tools");
    return Response.json({ tools: [search] });
  });
  const result = await api.getSkills("/work", null, controller.signal);
  assert.deepEqual(result, {
    skills: [
      {
        name: "mcp:web tools.search",
        mcp_server: "web tools",
        mcp_tool: "search",
        description: "Search the web",
        activate: "shelley mcp list 'web tools.search' -schema",
        origin: "MCP tool",
        source_path: "web tools",
        parameters: search.inputSchema,
      },
    ],
    warnings: [],
  });
  assert.equal(requests.length, 3);
  assert.ok(requests.slice(0, 2).every((request) => request.signal === controller.signal));
  assert.equal(requests[2].signal?.aborted, false);
  controller.abort();
  assert.equal(requests[2].signal?.aborted, true);
});

test("one inaccessible MCP server warns without hiding skills or another server's tools", async (t) => {
  const skill = { name: "review", description: "Review text", activate: "load review" };
  t.mock.method(globalThis, "fetch", async (input: string) => {
    if (input.startsWith("/api/skills?")) return Response.json({ skills: [skill] });
    if (input === "/api/mcp/servers") return Response.json([{ name: "locked" }, { name: "web" }]);
    if (input.endsWith("/locked/tools")) return new Response("Log in to locked", { status: 401 });
    return Response.json({ tools: [search] });
  });
  const result = await api.getSkills("/work", null);
  assert.deepEqual(
    result.skills.map((skill) => skill.name),
    ["review", "mcp:web.search"],
  );
  assert.deepEqual(result.warnings, ["locked: Failed to list tools: Log in to locked"]);
});

test("MCP registration lookup errors warn while ordinary skills remain available", async (t) => {
  const skills = [{ name: "review", description: "Review text", activate: "load review" }];
  t.mock.method(globalThis, "fetch", async (input: string) =>
    input.startsWith("/api/skills?")
      ? Response.json({ skills })
      : new Response("Offline", { status: 503 }),
  );
  assert.deepEqual(await api.getSkills("/work", null), {
    skills,
    warnings: ["Failed to load MCP servers: Offline"],
  });
});

test("a stalled server times out after five seconds without hiding healthy tools or skills", async (t) => {
  const deadlines: AbortController[] = [];
  t.mock.method(AbortSignal, "timeout", (milliseconds: number) => {
    assert.equal(milliseconds, 5000);
    const deadline = new AbortController();
    deadlines.push(deadline);
    return deadline.signal;
  });
  let started!: () => void;
  const stalled = new Promise<void>((resolve) => {
    started = resolve;
  });
  const skill = { name: "review", description: "Review text", activate: "load review" };
  t.mock.method(globalThis, "fetch", async (input: string, init?: RequestInit) => {
    if (input.startsWith("/api/skills?")) return Response.json({ skills: [skill] });
    if (input === "/api/mcp/servers") return Response.json([{ name: "web" }, { name: "slow" }]);
    if (input.endsWith("/web/tools")) return Response.json({ tools: [search] });
    return new Promise<Response>((_resolve, reject) => {
      init?.signal?.addEventListener("abort", () => reject(init.signal?.reason), { once: true });
      started();
    });
  });
  const pending = api.getSkills("/work", null);
  await stalled;
  assert.equal(deadlines.length, 2);
  deadlines[1].abort(new DOMException("Timed out listing tools", "TimeoutError"));
  const result = await pending;
  assert.deepEqual(
    result.skills.map((skill) => skill.name),
    ["review", "mcp:web.search"],
  );
  assert.deepEqual(result.warnings, ["slow: Timed out listing tools"]);
});

test("tool names cannot inject newlines or backticks into activation instructions", async (t) => {
  t.mock.method(globalThis, "fetch", async (input: string) => {
    if (input.startsWith("/api/skills?")) return Response.json({ skills: [] });
    if (input === "/api/mcp/servers") return Response.json([{ name: "web" }]);
    return Response.json({
      tools: [search, { ...search, name: "bad`name" }, { ...search, name: "bad\nname" }],
    });
  });
  const result = await api.getSkills("/work", null);
  assert.deepEqual(
    result.skills.map((skill) => skill.name),
    ["mcp:web.search"],
  );
  assert.deepEqual(result.warnings, ["web: skipped tools with invalid names"]);
});

test("aborting MCP discovery rejects instead of returning an authorization warning", async (t) => {
  const controller = new AbortController();
  t.mock.method(globalThis, "fetch", async (input: string) => {
    if (input.startsWith("/api/skills?")) return Response.json({ skills: [] });
    if (input === "/api/mcp/servers") return Response.json([{ name: "web" }]);
    controller.abort();
    throw controller.signal.reason;
  });
  await assert.rejects(api.getSkills("/work", null, controller.signal), { name: "AbortError" });
});

for (const mode of ["new", "send", "queue"] as const) {
  test(`${mode} sends exact tool instructions, preserves plain skills, settings and +Profile prefix`, async (t) => {
    let posted: Record<string, unknown> | undefined;
    t.mock.method(globalThis, "fetch", async (_input: string, init?: RequestInit) => {
      posted = JSON.parse(String(init?.body));
      return Response.json({ conversation_id: "new-id" });
    });
    const request = {
      message: "+Research find sources",
      skills: ["review", "mcp:web.search", "mcp:files.read"],
      model: "model-a",
      queue: mode === "queue",
    };
    const original = structuredClone(request);
    if (mode === "new") await api.sendMessageWithNewConversation(request);
    else await api.sendMessage("existing-id", request);
    assert.deepEqual(request, original);
    assert.deepEqual(posted?.skills, ["review"]);
    assert.equal(posted?.model, "model-a");
    assert.equal(posted?.queue, mode === "queue");
    assert.match(String(posted?.message), /^\+Research find sources\n\n/);
    assert.match(String(posted?.message), /Use the MCP tool "web\.search"/);
    assert.match(String(posted?.message), /shelley mcp list 'web\.search' -schema/);
    assert.match(String(posted?.message), /Use the MCP tool "files\.read"/);
    assert.match(String(posted?.message), /shelley mcp call/);
  });
}

test("ordinary skill submissions are unchanged", async (t) => {
  const request = { message: "Review this", skills: ["review"] };
  let posted: unknown;
  t.mock.method(globalThis, "fetch", async (_input: string, init?: RequestInit) => {
    posted = JSON.parse(String(init?.body));
    return Response.json({});
  });
  await api.sendMessage("existing-id", request);
  assert.deepEqual(posted, request);
});
