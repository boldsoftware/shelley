// markdownRender tests: the sanitize pipeline, plus the WeakMap-owner-scoped
// render cache. Run via `pnpm test` (see scripts/run-tests.mjs).
//
// DOMPurify needs a real `window`/`document` in Node (it auto-detects the
// browser global otherwise). Set that up before importing markdownRender, the
// same way ansi.test.ts does.
import { JSDOM } from "jsdom";
import DOMPurify from "dompurify";
import { coalesceContent } from "./coalesceContent";

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

// ---- Cited text ending at a closing code fence, without a newline ----

{
  const [item] = coalesceContent([
    {
      ID: "",
      Type: 2,
      Text: "```\ncode\n```",
      Citations: [{ url: "https://example.com", title: "Source" }],
    },
  ]);
  const root = JSDOM.fragment(renderMarkdownToSafeHTML(item.markdownText));
  assert(
    root.querySelector("pre > code")?.textContent === "code\n",
    "citation HTML does not become part of the code block",
  );
  assert(
    root.querySelector(".citation-refs > a.citation-ref")?.closest("pre, code") === null,
    "citation renders as a real link outside the code block",
  );
}

// ---- Obsidian open links retain their URI without allowing arbitrary schemes ----

for (const url of [
  "obsidian://open?vault=Notes&file=Folder%2FMy%20note.md",
  "obsidian://open?path=%2FUsers%2Fme%2FNotes%2FMy%20note.md",
  "obsidian://open?vault=Notes&file=My%20note%23Heading",
  "obsidian://open?vault=My%20Notes&file=Note%23%5EBlock",
  "OBSIDIAN://open?vault=Notes&file=Note",
]) {
  for (const markdown of [
    `[Note](${url})`,
    `<${url}>`,
    `<a href="${url}" onclick="evil()">Note</a>`,
  ]) {
    const root = JSDOM.fragment(renderMarkdownToSafeHTML(markdown));
    const link = root.querySelector("a");
    assert(link?.getAttribute("href") === url, `Obsidian URI preserved: ${markdown}`);
    assert(
      link?.getAttribute("target") === "_blank" &&
        link?.getAttribute("rel") === "noopener noreferrer" &&
        !link?.hasAttribute("onclick"),
      "Obsidian links keep standard link protections and strip event handlers",
    );
  }
}

for (const url of [
  "javascript:evil()",
  "JaVaScRiPt:evil()",
  "java&#x09;script:evil()",
  "javascript:obsidian://open?vault=Notes",
  "vbscript:evil()",
  "data:text/html,evil",
  "file:///tmp/note.md",
  "unknown://open",
  "obsidian://new?vault=Notes&file=Note",
  "obsidian://daily?vault=Notes",
  "obsidian://advanced-uri?vault=Notes&commandid=evil",
  "obsidian://vault/Notes/Note",
  "obsidian:///tmp/note.md",
  "obsidian://open",
  "obsidian://open.evil?vault=Notes",
  "obsidian://open@evil?vault=Notes",
  "obsidian-evil://open?vault=Notes",
]) {
  const root = JSDOM.fragment(renderMarkdownToSafeHTML(`<a href="${url}">Note</a>`));
  assert(root.querySelector("a")?.hasAttribute("href") === false, `URI blocked: ${url}`);
}

for (const url of [
  "http://example.com",
  "https://example.com",
  "ftp://example.com",
  "ftps://example.com",
  "mailto:me@example.com",
  "tel:123",
  "callto:123",
  "sms:123",
  "cid:note",
  "xmpp:me@example.com",
  "matrix:roomid/room",
  "//example.com",
  "/note",
  "./note",
  "../note",
  "#note",
]) {
  const root = JSDOM.fragment(renderMarkdownToSafeHTML(`[Note](${url})`));
  assert(root.querySelector("a")?.getAttribute("href") === url, `Existing URI allowed: ${url}`);
}

for (const markdown of [
  "![Note](obsidian://open?vault=Notes&file=Note)",
  '<img src="obsidian://open?vault=Notes&file=Note">',
]) {
  assert(
    !JSDOM.fragment(renderMarkdownToSafeHTML(markdown, "msg-1")).querySelector("img"),
    "Obsidian URIs are not loaded as images",
  );
}

{
  const url = "obsidian://open?vault=Notes&file=Note";
  const root = JSDOM.fragment(renderMarkdownToSafeHTML(url));
  assert(!root.querySelector("a"), "bare Obsidian URLs are not linkified");
  assert(root.textContent?.trim() === url, "bare Obsidian URLs stay literal");
}

// ---- Standalone inline-code URLs are links, not commands or code blocks ----

for (const url of [
  "https://phil-dev.exe.xyz:3979/",
  "http://localhost:8000/path?q=one&other=two#fragment",
  "https://example.com/path_(with_parentheses)?q=a,b!",
  'https://example.com/?literal=&amp;&quote="value"',
  "https://example.com/?q=<script>alert(1)</script>",
  "HTTPS://EXAMPLE.COM/path",
  "http://[::1]:8000/",
]) {
  const root = JSDOM.fragment(renderMarkdownToSafeHTML(`Preview: \`${url}\``));
  const link = root.querySelector("a");
  assert(link?.getAttribute("href") === url, `inline-code URL is linked intact: ${url}`);
  assert(
    link?.querySelector("code")?.textContent === url,
    "linked URL keeps code markup and literal text",
  );
  assert(
    link?.getAttribute("target") === "_blank" &&
      link?.getAttribute("rel") === "noopener noreferrer",
    "inline-code link has the same new-tab protections as other links",
  );
  assert(!root.querySelector("script"), "URL content cannot inject HTML");
}

for (const code of [
  "const answer = 42;",
  "curl https://example.com",
  "https://example.com --flag",
  "https://one.example https://two.example",
  "javascript:alert(1)",
  "data:text/html,<script>alert(1)</script>",
  "file:///tmp/example.txt",
  "obsidian://open?vault=Notes&file=Note",
  "ftp://example.com/file",
  "//example.com/path",
  "https://",
  "https://example.com:invalid/",
  "https://[invalid]/",
]) {
  const root = JSDOM.fragment(renderMarkdownToSafeHTML(`\`${code}\``));
  assert(!root.querySelector("a"), `non-URL code is not linked: ${code}`);
  assert(root.querySelector("code")?.textContent === code, "non-URL code is unchanged");
}

for (const markdown of [
  "```\nhttps://example.com/fenced\n```",
  "```text\nhttps://example.com/fenced\n```",
  "    https://example.com/indented",
  "<pre><code>https://example.com/raw-block</code></pre>",
]) {
  const root = JSDOM.fragment(renderMarkdownToSafeHTML(markdown));
  assert(!!root.querySelector("pre > code"), "code block is preserved");
  assert(!root.querySelector("a"), "code block URLs are not linked");
}

for (const markdown of [
  "[`https://example.com/label`](https://example.com/destination)",
  "[**`https://example.com/label`**](https://example.com/destination)",
  '<a href="https://example.com/destination">`https://example.com/label`</a>',
]) {
  const root = JSDOM.fragment(renderMarkdownToSafeHTML(markdown));
  assert(
    root.querySelectorAll("a").length === 1,
    "code inside an existing link gets no nested link",
  );
  assert(
    root.querySelector("a")?.getAttribute("href") === "https://example.com/destination" &&
      root.querySelector("a code")?.textContent === "https://example.com/label",
    "explicit link destination and code label are preserved",
  );
}

// Local-image rewriting is keyed by messageId, independent of caching.
const withId = renderMarkdownToSafeHTML("![alt](out/plot.png)", "msg-1");
assert(
  withId.includes("/api/message/msg-1/file?path=out%2Fplot.png"),
  "local image rewritten to per-message file endpoint",
);
const withoutId = renderMarkdownToSafeHTML("![alt](out/plot.png)");
assert(!withoutId.includes("<img"), "local image dropped with no messageId to authorize it");

// sandbox: links (a ChatGPT convention the system prompt adopts) download the
// file through the per-message download endpoint.
{
  const root = document.createElement("div");
  root.innerHTML = renderMarkdownToSafeHTML("[get it](<sandbox:/tmp/out/a b.mp4>)", "msg-1");
  const a = root.querySelector("a");
  assert(
    a?.getAttribute("href") === "/api/message/msg-1/download?path=%2Ftmp%2Fout%2Fa%20b.mp4" &&
      a?.getAttribute("download") === "" &&
      !a?.hasAttribute("target") &&
      a?.textContent === "get it",
    "sandbox: link rewritten to a same-tab download",
  );
  root.innerHTML = renderMarkdownToSafeHTML("[get it](sandbox:/tmp/a.mp4)");
  assert(
    root.querySelector("a")?.hasAttribute("href") === false,
    "sandbox: link is inert with no messageId to authorize it",
  );
  root.innerHTML = renderMarkdownToSafeHTML('<a href="/x" download="evil.sh">x</a>', "msg-1");
  assert(
    root.querySelector("a")?.hasAttribute("download") === false,
    "raw HTML cannot mark arbitrary links as downloads",
  );
}

const exeDevLinks = { isExeDev: true, hostname: "demo.exe.xyz" };
const rewrittenMarkdownLink = renderMarkdownToSafeHTML(
  "[Open app](http://localhost:3000/path?q=one#two)",
  undefined,
  undefined,
  exeDevLinks,
);
assert(
  rewrittenMarkdownLink.includes('href="https://demo.exe.xyz:3000/path?q=one#two"') &&
    rewrittenMarkdownLink.includes(">Open app</a>"),
  "opted-in markdown links use the public exe.dev URL",
);
const rewrittenAutolink = renderMarkdownToSafeHTML(
  "http://127.0.0.1:4321/live",
  undefined,
  undefined,
  exeDevLinks,
);
assert(
  rewrittenAutolink.includes('href="https://demo.exe.xyz:4321/live"') &&
    rewrittenAutolink.includes(">https://demo.exe.xyz:4321/live</a>"),
  "opted-in markdown autolinks rewrite both href and visible URL",
);
const unrewrittenMarkdownLink = renderMarkdownToSafeHTML("[Open app](http://localhost:3000/path)");
assert(
  unrewrittenMarkdownLink.includes('href="http://localhost:3000/path"') &&
    unrewrittenMarkdownLink.includes(">Open app</a>"),
  "markdown links are unchanged without the opt-in",
);
const localURLCode = renderMarkdownToSafeHTML(
  "`http://localhost:3000/inline`\n\n```\nhttp://localhost:3000/fenced\n```",
  undefined,
  undefined,
  exeDevLinks,
);
assert(
  localURLCode.includes('href="https://demo.exe.xyz:3000/inline"') &&
    localURLCode.includes("<code>https://demo.exe.xyz:3000/inline</code>") &&
    localURLCode.includes("<pre><code>http://localhost:3000/fenced\n</code></pre>"),
  "standalone inline-code URLs use the opt-in localhost rewrite, but fenced code is unchanged",
);
const remoteLocalhostImage = renderMarkdownToSafeHTML(
  "![plot](http://localhost:3000/plot.png)",
  undefined,
  undefined,
  exeDevLinks,
);
assert(
  !remoteLocalhostImage.includes("demo.exe.xyz") && !remoteLocalhostImage.includes("<img"),
  "image sources are not rewritten into user-facing links",
);

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
