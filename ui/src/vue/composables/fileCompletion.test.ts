import assert from "node:assert/strict";
import { test, type TestContext } from "node:test";
import { effectScope, ref } from "vue";
import type { api } from "../../services/api";
import { useFileCompletion } from "./fileCompletion";

type Response = Awaited<ReturnType<typeof api.findFiles>>;
function harness(t: TestContext) {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const message = ref("@");
  const cwd = ref("/work");
  const session = ref("one");
  const enabled = ref(true);
  const requests: {
    cwd: string;
    query: string;
    signal?: AbortSignal;
    resolve: (result: Response) => void;
    reject: (error: Error) => void;
  }[] = [];
  const scope = effectScope();
  const completion = scope.run(() =>
    useFileCompletion({
      message,
      cwd: () => cwd.value,
      session: () => session.value,
      enabled: () => enabled.value,
      findFiles: (dir, query, signal, opts) => {
        assert.equal(opts?.content, "skip");
        return new Promise<Response>((resolve, reject) => {
          requests.push({ cwd: dir, query, signal, resolve, reject });
        });
      },
    }),
  )!;
  t.after(() => scope.stop());
  completion.updateSelection(1, 1);
  completion.focused.value = true;
  function input(text: string) {
    message.value = text;
    completion.updateSelection(text.length, text.length);
  }
  return { completion, input, message, cwd, session, enabled, requests, scope };
}

function response(path: string, searchDir = "/searched"): Response {
  return {
    dir: "/work",
    search_dir: searchDir,
    query: "",
    match_query: "",
    matches: [{ path }],
    total: 1,
    truncated: false,
  };
}

test("debounces typing and inserts a quoted path without fetching contents", async (t) => {
  const h = harness(t);
  h.input("@f");
  h.input("@fi");
  assert.equal(h.requests.length, 0);
  t.mock.timers.tick(120);
  assert.equal(h.requests.length, 1);
  assert.equal(h.requests[0].cwd, "/work");
  assert.equal(h.requests[0].query, "fi");
  h.requests[0].resolve(response("File With Spaces.md"));
  await Promise.resolve();
  assert.equal(h.completion.loading.value, false);
  assert.equal(h.completion.matches.value.length, 1);
  assert.deepEqual(h.completion.choose(0), {
    text: '"/searched/File With Spaces.md" ',
    cursor: 32,
  });
  assert.equal(h.completion.visible.value, false);
});

test("old responses cannot replace new results, even if the transport ignores abort", async (t) => {
  const h = harness(t);
  t.mock.timers.tick(120);
  h.input("@new");
  assert.equal(h.requests[0].signal?.aborted, true);
  t.mock.timers.tick(120);
  h.requests[1].resolve(response("new.md"));
  await Promise.resolve();
  h.requests[0].resolve(response("old.md"));
  await Promise.resolve();
  assert.equal(h.completion.matches.value[0].path, "new.md");
  h.input("email user@example.com");
  assert.equal(h.completion.visible.value, false);
  assert.equal(h.completion.choose(0), null);
});

test("directory and conversation changes invalidate results and issue scoped requests", async (t) => {
  const h = harness(t);
  t.mock.timers.tick(120);
  h.cwd.value = "/different";
  h.session.value = "two";
  assert.equal(h.requests[0].signal?.aborted, true);
  assert.equal(h.completion.choose(0), null);
  t.mock.timers.tick(120);
  assert.equal(h.requests[1].cwd, "/different");
  h.requests[0].reject(new Error("stale failure"));
  await Promise.resolve();
  assert.equal(h.completion.error.value, "");
  h.requests[1].resolve(response("current.md"));
  await Promise.resolve();
  assert.equal(h.completion.matches.value[0].path, "current.md");
});

test("Escape dismissal survives cursor resync but typing a new query reopens", (t) => {
  const h = harness(t);
  h.input("@file");
  h.completion.dismiss();
  h.completion.updateSelection(3, 3);
  h.completion.focused.value = false;
  h.completion.focused.value = true;
  assert.equal(h.completion.visible.value, false);
  t.mock.timers.tick(120);
  assert.equal(h.requests.length, 0);
  h.input("@x");
  assert.equal(h.completion.visible.value, true);
  t.mock.timers.tick(120);
  assert.equal(h.requests.length, 1);
});

test("errors are surfaced; empty results and loading cannot complete a file", async (t) => {
  const h = harness(t);
  assert.equal(h.completion.choose(0), null);
  t.mock.timers.tick(120);
  h.requests[0].reject(new Error("Permission denied"));
  await Promise.resolve();
  assert.equal(h.completion.error.value, "Permission denied");
  assert.equal(h.completion.loading.value, false);
  h.input("@empty");
  t.mock.timers.tick(120);
  h.requests[1].resolve({ ...response(""), matches: [], total: 0 });
  await Promise.resolve();
  assert.equal(h.completion.error.value, "");
  assert.equal(h.completion.matches.value.length, 0);
  assert.equal(h.completion.choose(0), null);
});

test("blur, missing cwd, disabled composer, and disposal cancel pending work", (t) => {
  const h = harness(t);
  t.mock.timers.tick(120);
  h.completion.focused.value = false;
  assert.equal(h.requests[0].signal?.aborted, true);
  assert.equal(h.completion.visible.value, false);
  h.cwd.value = "";
  h.completion.focused.value = true;
  assert.equal(h.completion.visible.value, false);
  h.cwd.value = "/work";
  h.enabled.value = false;
  assert.equal(h.completion.visible.value, false);
  t.mock.timers.tick(120);
  assert.equal(h.requests.length, 1);
  h.enabled.value = true;
  h.scope.stop();
  t.mock.timers.tick(120);
  assert.equal(h.requests.length, 1);
});
