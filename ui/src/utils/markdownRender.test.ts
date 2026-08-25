// markdownRender tests: the sanitize pipeline, plus the WeakMap-owner-scoped
// render cache. Run via `pnpm test` (see scripts/run-tests.mjs).
//
// DOMPurify needs a real `window`/`document` in Node (it auto-detects the
// browser global otherwise). Set that up before importing markdownRender, the
// same way ansi.test.ts does.
import { JSDOM } from "jsdom";
import DOMPurify from "dompurify";

const dom = new JSDOM("");
const g = globalThis as Record<string, unknown>;
g.window = dom.window;
g.document = dom.window.document;
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const purify = DOMPurify(dom.window as any);
Object.assign(DOMPurify, purify);

const { renderMarkdownToSafeHTML } = await import("./markdownRender");

let passed = 0;
let failed = 0;
function assert(cond: boolean, msg: string): void {
  if (cond) {
    passed++;
  } else {
    failed++;
    console.error(`FAIL: ${msg}`);
  }
}

// Count actual parse+sanitize invocations via DOMPurify.sanitize, which every
// non-cache-hit call must go through exactly once.
const origSanitize = DOMPurify.sanitize;
let calls = 0;
function withCallCounting<T>(fn: () => T): T {
  calls = 0;
  DOMPurify.sanitize = ((...args: Parameters<typeof origSanitize>) => {
    calls++;
    return origSanitize(...args);
  }) as typeof DOMPurify.sanitize;
  try {
    return fn();
  } finally {
    DOMPurify.sanitize = origSanitize;
  }
}

// ---- Baseline: rendering + sanitization behavior is unchanged ----

assert(
  renderMarkdownToSafeHTML("# Title\n\nSome **bold** text.").includes("<strong>bold</strong>"),
  "basic markdown renders bold",
);
const fencedCode = renderMarkdownToSafeHTML("```typescript\nconst answer = 42;\n```");
assert(
  fencedCode.includes('<pre><code class="language-typescript">const answer = 42;\n</code></pre>'),
  "fenced code preserves its language class for post-render highlighting",
);
const unknownFencedCode = renderMarkdownToSafeHTML("```unknown-language\nplain text\n```");
assert(
  unknownFencedCode.includes(
    '<pre><code class="language-unknown-language">plain text\n</code></pre>',
  ),
  "unknown fenced code keeps its source and language class for a plain render",
);
assert(
  renderMarkdownToSafeHTML("```\nplain text\n```").includes("<pre><code>plain text\n</code></pre>"),
  "unlabeled fenced code remains plain",
);
assert(
  renderMarkdownToSafeHTML("<script>alert(1)</script>hello").includes("hello") &&
    !renderMarkdownToSafeHTML("<script>alert(1)</script>hello").includes("<script>"),
  "raw script tags are stripped by sanitization",
);

// Local-image rewriting is keyed by messageId, independent of caching.
const withId = renderMarkdownToSafeHTML("![alt](out/plot.png)", "msg-1");
assert(
  withId.includes("/api/message/msg-1/file?path=out%2Fplot.png"),
  "local image rewritten to per-message file endpoint",
);
const withoutId = renderMarkdownToSafeHTML("![alt](out/plot.png)");
assert(!withoutId.includes("<img"), "local image dropped with no messageId to authorize it");

// ---- Same owner + same runKey: a cache hit, no re-parse ----

{
  const owner = {};
  const text = "# Hello\n\nWorld, with `code` and a [link](https://example.com).";
  let first = "";
  let second = "";
  withCallCounting(() => {
    first = renderMarkdownToSafeHTML(text, "msg-cache-1", { owner, runKey: "0" });
  });
  assert(calls === 1, "first render for a (owner, runKey) parses markdown");
  withCallCounting(() => {
    second = renderMarkdownToSafeHTML(text, "msg-cache-1", { owner, runKey: "0" });
  });
  assert(calls === 0, "second render for the same (owner, runKey) is a cache hit (no re-parse)");
  assert(first === second, "cache hit returns the same HTML string");
}

// ---- Distinct run keys under the same owner: a message with multiple
// markdown runs (coalesceContent splits interleaved text blocks) must not let
// one run's cache entry answer for another's. ----

{
  const owner = {};
  let runA = "";
  let runB = "";
  withCallCounting(() => {
    runA = renderMarkdownToSafeHTML("first run of the message", "msg-multi", {
      owner,
      runKey: "0",
    });
    runB = renderMarkdownToSafeHTML("second run of the same message", "msg-multi", {
      owner,
      runKey: "1",
    });
  });
  assert(calls === 2, "two distinct run keys under one owner each parse once");
  assert(runA !== runB, "distinct run keys render distinct HTML, not one clobbering the other");
  assert(runA.includes("first run"), "run 0's cached entry keeps its own text");
  assert(runB.includes("second run"), "run 1's cached entry keeps its own text");

  withCallCounting(() => {
    const runA2 = renderMarkdownToSafeHTML("first run of the message", "msg-multi", {
      owner,
      runKey: "0",
    });
    const runB2 = renderMarkdownToSafeHTML("second run of the same message", "msg-multi", {
      owner,
      runKey: "1",
    });
    assert(runA2 === runA, "remounting run 0 hits its own cache entry");
    assert(runB2 === runB, "remounting run 1 hits its own cache entry");
  });
  assert(calls === 0, "remounting both runs is served entirely from cache");
}

// ---- Distinct owners: two different Message objects must not share cache
// entries, even under the same runKey — e.g. reopening an old conversation
// (oldest-first) must not collide with a never-evicted newer one. ----

{
  const ownerA = {};
  const ownerB = {};
  const img = "![alt](pic.png)";
  const rA = renderMarkdownToSafeHTML(img, "msg-A", { owner: ownerA, runKey: "0" });
  const rB = renderMarkdownToSafeHTML(img, "msg-B", { owner: ownerB, runKey: "0" });
  assert(
    rA.includes("/api/message/msg-A/file") && !rA.includes("msg-B"),
    "owner A's cached render points at message A's file endpoint",
  );
  assert(
    rB.includes("/api/message/msg-B/file") && !rB.includes("msg-A"),
    "owner B's cached render points at message B's file endpoint (not A's, stale)",
  );

  withCallCounting(() => {
    renderMarkdownToSafeHTML(img, "msg-A", { owner: ownerA, runKey: "0" });
    renderMarkdownToSafeHTML(img, "msg-B", { owner: ownerB, runKey: "0" });
  });
  assert(calls === 0, "both owners' entries are independently cached and both hit");
}

// ---- No owner: never cached, by design. The streaming preview and
// distillation preview call without a cacheKey, so identical text across
// calls must still re-render (it may not be immutable). ----

{
  withCallCounting(() => {
    renderMarkdownToSafeHTML("streaming so far");
    renderMarkdownToSafeHTML("streaming so far");
  });
  assert(calls === 2, "calls without a cacheKey are never cached (each one re-parses)");
}

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
