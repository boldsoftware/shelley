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
    opts?: { content?: "skip" | "only"; includeDirs?: boolean };
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
        if (opts?.content === "skip") assert.equal(opts.includeDirs, true);
        else assert.equal(opts?.content, "only");
        return new Promise<Response>((resolve, reject) => {
          requests.push({ cwd: dir, query, signal, opts, resolve, reject });
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

function response(path: string, searchDir = "/work"): Response {
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

test("debounces typing and inserts a relative path while content search continues", async (t) => {
  const h = harness(t);
  h.input("@f");
  h.input("@fi");
  assert.equal(h.requests.length, 0);
  t.mock.timers.tick(120);
  assert.equal(h.requests.length, 2);
  assert.equal(h.requests[0].cwd, "/work");
  assert.equal(h.requests[0].query, "fi");
  assert.equal(h.requests[0].opts?.content, "skip");
  assert.equal(h.requests[1].opts?.content, "only");
  h.requests[0].resolve(response("File With Spaces.md"));
  await Promise.resolve();
  assert.equal(h.completion.loading.value, false);
  assert.equal(h.completion.grepPending.value, true);
  assert.equal(h.completion.matches.value.length, 1);
  const replacement = h.completion.choose(0)!;
  assert.deepEqual(replacement, {
    text: '@"File With Spaces.md" ',
    cursor: 23,
  });
  assert.equal(h.completion.visible.value, false);
  h.message.value = replacement.text;
  h.completion.updateSelection(replacement.cursor, replacement.cursor);
  t.mock.timers.tick(120);
  assert.equal(h.completion.visible.value, false);
  assert.equal(h.requests.length, 2);
});

test("content hits annotate name matches and append files selected by their contents", async (t) => {
  const h = harness(t);
  h.input("@needle");
  t.mock.timers.tick(120);
  assert.equal(h.requests.length, 2);

  const names = response("README.md", "/work/docs");
  names.matches[0].matched_indexes = [0, 1];
  h.requests[0].resolve(names);
  await Promise.resolve();
  assert.equal(h.completion.loading.value, false);
  assert.equal(h.completion.grepPending.value, true);
  assert.deepEqual(h.completion.matches.value[0].matched_indexes, [5, 6]);

  const contents = response("README.md", "/work/docs");
  contents.matches = [
    {
      path: "README.md",
      line: 4,
      snippet: "contains needle here",
      snippet_matched_indexes: [9, 10, 11, 12, 13, 14],
    },
    {
      path: "src/other.ts",
      line: 9,
      snippet: "const needle = true",
      snippet_matched_indexes: [6, 7, 8, 9, 10, 11],
    },
  ];
  contents.total = 2;
  h.requests[1].resolve(contents);
  await Promise.resolve();
  await Promise.resolve();

  assert.equal(h.completion.grepPending.value, false);
  assert.deepEqual(
    h.completion.matches.value.map((match) => [match.path, match.line, match.snippet]),
    [
      ["docs/README.md", 4, "contains needle here"],
      ["docs/src/other.ts", 9, "const needle = true"],
    ],
  );
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
  h.requests[1].resolve(response("current.md", "/different"));
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
  assert.equal(h.requests.length, 2);
});

test("name errors are surfaced; empty results and loading cannot complete a file", async (t) => {
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

test("content errors leave name results intact", async (t) => {
  const h = harness(t);
  h.input("@file");
  t.mock.timers.tick(120);
  h.requests[0].resolve(response("file.md"));
  await Promise.resolve();
  h.requests[1].reject(new Error("grep failed"));
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(h.completion.error.value, "");
  assert.equal(h.completion.grepPending.value, false);
  assert.equal(h.completion.matches.value[0].path, "file.md");
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

test("folders insert a relative reference with a trailing slash and preserve surrounding text", async (t) => {
  const h = harness(t);
  h.message.value = "Read @notes, please";
  h.completion.updateSelection(11, 11);
  t.mock.timers.tick(120);
  const folder = response("My Notes/", "/elsewhere");
  folder.matches[0].is_dir = true;
  h.requests[0].resolve(folder);
  await Promise.resolve();
  assert.equal(h.completion.matches.value[0].is_dir, true);
  const replacement = h.completion.choose(0)!;
  assert.equal(replacement.text, 'Read @"../elsewhere/My Notes/", please');
  assert.equal(replacement.text.slice(replacement.cursor), ", please");
  h.message.value = replacement.text;
  h.completion.updateSelection(replacement.cursor, replacement.cursor);
  t.mock.timers.tick(120);
  assert.equal(h.completion.visible.value, false);
  assert.equal(h.requests.length, 2);
});
