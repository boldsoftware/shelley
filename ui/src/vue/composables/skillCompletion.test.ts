import assert from "node:assert/strict";
import { test, type TestContext } from "node:test";
import { effectScope, nextTick, ref } from "vue";
import type { api } from "../../services/api";
import { useSkillCompletion } from "./skillCompletion";

type Response = Awaited<ReturnType<typeof api.getSkills>>;
const response = (...names: string[]): Response => ({
  skills: names.map((name) => ({
    name,
    description: `Help with ${name}`,
    activate: `load ${name}`,
  })),
});

async function harness(t: TestContext) {
  const message = ref("/skills");
  const cwd = ref("/work");
  const session = ref<string | null>(null);
  const conversationId = ref<string | null>(null);
  const requests: {
    cwd: string;
    conversationId: string | null;
    signal?: AbortSignal;
    resolve: (r: Response) => void;
    reject: (e: Error) => void;
  }[] = [];
  const scope = effectScope();
  const completion = scope.run(() =>
    useSkillCompletion({
      message,
      cwd: () => cwd.value,
      session: () => session.value,
      conversationId: () => conversationId.value,
      enabled: () => true,
      getSkills: (cwd, conversationId, signal) =>
        new Promise<Response>((resolve, reject) =>
          requests.push({ cwd, conversationId, signal, resolve, reject }),
        ),
    }),
  )!;
  t.after(() => scope.stop());
  completion.updateSelection(7, 7);
  completion.focused.value = true;
  function input(text: string) {
    message.value = text;
    completion.updateSelection(text.length, text.length);
  }
  await nextTick();
  // Resolve the first request and let the catalog land.
  async function load(...names: string[]) {
    requests[requests.length - 1].resolve(response(...names));
    await Promise.resolve();
    await Promise.resolve();
  }
  return { completion, input, message, cwd, session, conversationId, requests, load };
}

test("choose() returns the draft without /skills plus the skill, and nothing else", async (t) => {
  const h = await harness(t);
  assert.equal(h.completion.choose(0), null); // still loading
  await h.load("foo", "bar");
  h.input("/skills bar\nrest");
  h.completion.updateSelection(11, 11);
  const result = h.completion.choose(0)!;
  assert.equal(result.skill.name, "bar");
  assert.equal(result.text, "rest");
  assert.equal(result.cursor, 0);
  assert.equal(h.completion.visible.value, false);
});

test("one catalog serves close/reopen and query edits", async (t) => {
  const h = await harness(t);
  await h.load("foo", "bar");
  h.input("/skills HELP FOO");
  assert.deepEqual(
    h.completion.matches.value.map((s) => s.name),
    ["foo"],
  );
  h.completion.dismiss();
  assert.equal(h.completion.visible.value, false);
  h.completion.reopen();
  h.input("");
  h.input("/skills");
  await nextTick();
  assert.equal(h.requests.length, 1);
  assert.equal(h.completion.matches.value.length, 2);
});

test("a new cwd, session or conversation refetches and drops the old catalog", async (t) => {
  const h = await harness(t);
  await h.load("old");
  h.cwd.value = "/other";
  assert.equal(h.requests[0].signal?.aborted, true);
  assert.equal(h.completion.matches.value.length, 0);
  await nextTick();
  assert.equal(h.requests.length, 2);
  assert.equal(h.requests[1].cwd, "/other");
  h.conversationId.value = "c2";
  h.session.value = "c2";
  await nextTick();
  assert.equal(h.requests.length, 3);
  assert.equal(h.requests[2].conversationId, "c2");
  // A stale response cannot overwrite the current catalog.
  h.requests[2].resolve(response("current"));
  await Promise.resolve();
  h.requests[1].resolve(response("stale"));
  await Promise.resolve();
  assert.deepEqual(
    h.completion.matches.value.map((s) => s.name),
    ["current"],
  );
});

test("invalidate() forces a refetch on the next open", async (t) => {
  const h = await harness(t);
  await h.load("foo");
  h.completion.invalidate();
  assert.equal(h.completion.matches.value.length, 0);
  h.completion.dismiss();
  h.completion.reopen();
  await nextTick();
  assert.equal(h.requests.length, 2);
});

test("errors cannot select and the next open retries", async (t) => {
  const h = await harness(t);
  h.requests[0].reject(new Error("Permission denied"));
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(h.completion.error.value, "Permission denied");
  assert.equal(h.completion.loading.value, false);
  assert.equal(h.completion.choose(0), null);
  h.completion.dismiss();
  h.completion.reopen();
  await nextTick();
  assert.equal(h.requests.length, 2);
  assert.equal(h.completion.error.value, "");
  await h.load("foo");
  assert.equal(h.completion.matches.value.length, 1);
});

test("partial MCP warnings stay visible without blocking selection and reset with context", async (t) => {
  const h = await harness(t);
  h.requests[0].resolve({ ...response("mcp:web.search"), warnings: ["locked: log in"] });
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(h.completion.warning.value, "locked: log in");
  assert.equal(h.completion.error.value, "");
  assert.equal(h.completion.choose(0)?.skill.name, "mcp:web.search");
  h.cwd.value = "/other";
  assert.equal(h.completion.warning.value, "");
});

test("closing while MCP discovery is loading aborts it and reopening retries", async (t) => {
  const h = await harness(t);
  h.completion.dismiss();
  assert.equal(h.requests[0].signal?.aborted, true);
  assert.equal(h.completion.loading.value, false);
  h.completion.reopen();
  await nextTick();
  assert.equal(h.requests.length, 2);
  h.requests[0].resolve({ ...response("stale"), warnings: ["stale warning"] });
  await h.load("current");
  assert.deepEqual(
    h.completion.matches.value.map((skill) => skill.name),
    ["current"],
  );
  assert.equal(h.completion.warning.value, "");
});
