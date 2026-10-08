// Self-executing tests for system prompt template highlighting.

import { highlightTemplate, problemRange } from "./templateHighlight";

function assertEqual(actual: unknown, expected: unknown, message: string): void {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(
      `${message}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`,
    );
  }
}

function run(name: string, fn: () => void): void {
  fn();
  console.log(`✓ ${name}`);
}

run("actions are highlighted and prose escaped", () => {
  assertEqual(
    highlightTemplate(`a <b> {{if lt .X "<"}}`, null),
    `a &lt;b&gt; <span class="tpl-action"><span class="tpl-delim">{{</span>` +
      `<span class="tpl-keyword">if</span> <span class="tpl-keyword">lt</span> ` +
      `<span class="tpl-field">.X</span> <span class="tpl-string">"&lt;"</span>` +
      `<span class="tpl-delim">}}</span></span>`,
    "highlight",
  );
});

run("a trailing newline keeps its line", () => {
  assertEqual(highlightTemplate("a\n", null), "a\n ", "trailing newline");
});

run("a problem's column picks out its action", () => {
  const text = "Hi.\n{{.A}} {{.Nope}} after";
  assertEqual(problemRange(text, { line: 2, column: 10, message: "" }), [11, 20], "action");
  assertEqual(problemRange(text, { line: 2, message: "" }), [4, 26], "whole line");
  assertEqual(problemRange(text, { message: "" }), [0, 0], "nowhere");
});

run("columns count characters", () => {
  const text = "😀 {{.Nope}}";
  assertEqual(problemRange(text, { line: 1, column: 5, message: "" }), [3, 12], "astral");
});
