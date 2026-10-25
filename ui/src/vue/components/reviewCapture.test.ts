// DOM resolution for review recordings: breadcrumbs from data-review labels
// (across shadow roots, where Monaco puts its context menu), the text under
// the pointer, and selections. Monaco's own lines come from DiffViewer's
// ReviewContext, not from here.
import { JSDOM } from "jsdom";

const dom = new JSDOM(`<main id="root">
  <header data-review="Toolbar"><button id="next" aria-label="Next file (>)">›</button></header>
  <section data-review="Commits"><button id="commit" role="option">Rename value20</button></section>
  <section data-review="Changed files">
    <button id="file" role="treeitem" title="app.ts"><span>app.ts</span><span>+1 −1</span></button>
  </section>
  <div data-review="Files">
    <p id="prose">Select a diff   and file to view changes.</p>
    <div id="host"></div>
  </div>
  <section data-review="Notes">
    <p id="long">The recorder keeps every chunk in IndexedDB until the conversation accepts it, so a crash loses nothing. Stopping flushes the last chunk and the pointer timeline. Cancelling instead deletes <code>the stored session</code> outright and never uploads, which is why it asks twice before throwing anything away for good.</p>
    <pre id="body">first line
second   line here
third</pre>
    <div id="meta" data-review="Byline"><span id="author">Review Test</span></div>
    <ul><li id="run-on">${Array.from({ length: 60 }, (_, i) => `word${i}`).join(" ")}</li></ul>
  </section>
</main>`);
Object.assign(globalThis, {
  window: dom.window,
  document: dom.window.document,
  Element: dom.window.Element,
  HTMLElement: dom.window.HTMLElement,
  ShadowRoot: dom.window.ShadowRoot,
  Node: dom.window.Node,
  NodeFilter: dom.window.NodeFilter,
});

const { breadcrumb, collapse, describeNode, describeSelection, pointedText } =
  await import("./reviewCapture");

function assertEqual<T>(actual: T, expected: T, msg: string): void {
  const a = JSON.stringify(actual);
  const e = JSON.stringify(expected);
  if (a !== e) throw new Error(`${msg}: expected ${e}, got ${a}`);
}

function run(name: string, fn: () => void): void {
  try {
    fn();
    console.log(`\u2713 ${name}`);
  } catch (err) {
    console.error(`\u2717 ${name}`);
    throw err;
  }
}

// Monaco's context menu, in a shadow root inside the editor.
const host = document.getElementById("host")!;
const shadow = host.attachShadow({ mode: "open" });
shadow.innerHTML = `<div class="monaco-menu"><ul><li class="action-item">
  <a id="copy" class="action-menu-item" role="menuitem"><span class="action-label">Copy</span></a>
</li></ul></div>`;

run("collapses whitespace and truncates", () => {
  assertEqual(collapse("  a \n  b  "), "a b", "collapse");
  assertEqual(collapse("abcdef", 4), "abc\u2026", "truncate");
});

run("breadcrumbs nest outermost first and cross shadow roots", () => {
  assertEqual(breadcrumb(document.getElementById("author")), ["Notes", "Byline"], "nested");
  assertEqual(breadcrumb(shadow.getElementById("copy")), ["Files"], "shadow root");
});

run("describes prose, controls, and labelled regions", () => {
  assertEqual(
    describeNode(document.getElementById("prose")!.firstChild),
    { where: "Files", text: "Select a diff and file to view changes." },
    "prose block",
  );
  assertEqual(
    describeNode(document.getElementById("next")),
    { where: 'Toolbar › "Next file (>)"' },
    "control by its label",
  );
  assertEqual(
    describeNode(document.getElementById("commit")),
    { where: 'Commits › "Rename value20"' },
    "option by its text",
  );
  assertEqual(
    describeNode(document.getElementById("file")!.firstChild),
    { where: 'Changed files › "app.ts"' },
    "tree item by its title",
  );
  assertEqual(
    describeNode(shadow.querySelector(".action-label")),
    { where: 'Files › "Copy"' },
    "control in a shadow root",
  );
});

run("narrows long text to what is under the pointer", () => {
  const long = document.getElementById("long")!;
  const [opening, code, closing] = Array.from(long.childNodes);
  const caret = (node: ChildNode, word: string) => ({
    node: node as Text,
    offset: node.textContent!.indexOf(word),
  });
  assertEqual(
    pointedText(long, caret(opening, "flushes")),
    "Stopping flushes the last chunk and the pointer timeline.",
    "sentence",
  );
  assertEqual(
    pointedText(long, caret(closing, "asks")),
    "Cancelling instead deletes the stored session outright and never uploads, which is why it asks twice before throwing anything away for good.",
    "sentence across inline elements",
  );
  assertEqual(
    pointedText(long, caret(code.firstChild!, "stored")),
    pointedText(long, caret(closing, "asks")),
    "caret inside inline code",
  );
  assertEqual(pointedText(long, null).endsWith("\u2026"), true, "no caret: the clipped block");
  const body = document.getElementById("body")!.firstChild!;
  assertEqual(
    pointedText(document.getElementById("body")!, caret(body, "here")),
    "second line here",
    "preformatted line",
  );
  const prose = document.getElementById("prose")!;
  assertEqual(
    pointedText(prose, caret(prose.firstChild!, "file")),
    "Select a diff and file to view changes.",
    "short block stays whole",
  );
  const runOn = document.getElementById("run-on")!.firstChild!;
  const clipped = pointedText(runOn.parentElement!, caret(runOn, "word50"));
  assertEqual(
    [clipped.length <= 200, clipped.startsWith("…"), clipped.includes("word50")],
    [true, true, true],
    "overlong sentence clips around the pointer",
  );
  const offsets = [0, 1, 2, 3, 4].map((i) => ({ node: runOn as Text, offset: 150 + i }));
  assertEqual(
    new Set(offsets.map((c) => pointedText(runOn.parentElement!, c))).size,
    1,
    "the clip holds still as the pointer moves along",
  );
  const author = document.getElementById("author")!;
  assertEqual(
    describeNode(author, { node: author.firstChild as Text, offset: 2 }),
    { where: "Notes › Byline", text: "Review Test" },
    "text outside a block",
  );
  assertEqual(describeNode(author), { where: "Notes › Byline" }, "no caret, no block");
});

run("describes a selection and its clearing", () => {
  const selection = window.getSelection()!;
  const text = document.getElementById("prose")!.firstChild!;
  const range = document.createRange();
  range.setStart(text, 0);
  range.setEnd(text, 8);
  selection.removeAllRanges();
  selection.addRange(range);
  assertEqual(
    describeSelection(document.getElementById("root")!),
    { where: "Files", text: "Select a" },
    "prose selection",
  );
  selection.removeAllRanges();
  assertEqual(describeSelection(document.getElementById("root")!), null, "cleared selection");
});
