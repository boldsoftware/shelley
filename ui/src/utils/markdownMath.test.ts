import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import { collectMath, renderMath } from "./markdownMath";

const dom = new JSDOM();
Object.assign(globalThis, { window: dom.window, document: dom.window.document });
const { renderMarkdownToSafeHTML } = await import("./markdownRender");

function parseMath(markdown: string) {
  const root = JSDOM.fragment(renderMarkdownToSafeHTML(markdown));
  const nodes = collectMath(root);
  return { root, nodes };
}

function assertMath(markdown: string, expected: { text: string; displayMode: boolean }[]) {
  const result = parseMath(markdown);
  assert.deepEqual(
    result.nodes.map(({ text, displayMode }) => ({ text, displayMode })),
    expected,
    markdown,
  );
  return result;
}

for (const [open, close, displayMode] of [
  [String.raw`\(`, String.raw`\)`, false],
  [String.raw`\[`, String.raw`\]`, true],
  ["$", "$", false],
  ["$$", "$$", true],
] as const) {
  const text = String.raw`a_b + \|c_d\|^2`;
  const source = `${open}${text}${close}`;
  const { root, nodes } = assertMath(source, [{ text, displayMode }]);
  assert.equal(root.querySelector("em, strong"), null, "math is not parsed as Markdown");
  renderMath(nodes);
  assert.equal(root.querySelectorAll(".katex").length, 1, "KaTeX renders each delimiter form");
  assert.equal(root.querySelectorAll(".katex-display").length, Number(displayMode));
  assert.equal(root.querySelector(".katex-error"), null);

  assertMath(`Before ${source} after.`, [{ text, displayMode }]);
  assertMath(`${source} and ${source}`, [
    { text, displayMode },
    { text, displayMode },
  ]);
  for (const code of [`\`${source}\``, `\`\`\`tex\n${source}\n\`\`\``, `    ${source}`]) {
    const { root } = assertMath(code, []);
    assert.equal(root.querySelector("code")?.textContent?.trim(), source);
  }
  for (const code of [`<code>${source}</code>`, `<pre>${source}</pre>`]) {
    const { root } = assertMath(code, []);
    assert(root.querySelector("code, pre"));
  }
  for (const link of [
    `[${source}](https://example.com)`,
    `<a href="https://example.com">${source}</a>`,
  ]) {
    const { root } = assertMath(link, []);
    assert.equal(root.querySelectorAll("a").length, 1);
  }
}

// Regression: the original assistant equation must reach KaTeX unchanged.
const equation = String.raw`
\mathcal L(\theta)=
\underbrace{\mathcal L_{\mathrm{fit}}(f_\theta(x),y^\star)}_{\text{match reference solutions}}
+\lambda_h\|h(f_\theta(x),x)\|^2
+\lambda_g\|[g(f_\theta(x),x)]_+\|^2
`;
for (const [open, close] of [
  [String.raw`\[`, String.raw`\]`],
  ["$$", "$$"],
]) {
  for (const indent of ["", " ", "  ", "   "]) {
    const { root, nodes } = assertMath(`${indent}${open}${equation}${close}\n`, [
      { text: equation, displayMode: true },
    ]);
    renderMath(nodes);
    assert.equal(root.querySelectorAll(".katex-display").length, 1);
    assert.equal(root.querySelector(".katex-error"), null);
    assert.equal(root.querySelector("annotation")?.textContent, equation);
  }
}

assertMath(String.raw`\(x\) and $y$ and $$z$$ and \[w\]`, [
  { text: "x", displayMode: false },
  { text: "y", displayMode: false },
  { text: "z", displayMode: true },
  { text: "w", displayMode: true },
]);
assertMath(String.raw`\(x\)\(y\)`, [
  { text: "x", displayMode: false },
  { text: "y", displayMode: false },
]);
assertMath(String.raw`\[x\]\[y\]`, [
  { text: "x", displayMode: true },
  { text: "y", displayMode: true },
]);

assertMath("\\(x +\ny\\)", [{ text: "x +\ny", displayMode: false }]);
assertMath("\\[\r\nx + y\r\n\\] \t\r\n", [{ text: "\nx + y\n", displayMode: true }]);
{
  const { root } = assertMath("Before\n\\[\nx\n\n+ y\n\\]\nAfter", [
    { text: "\nx\n\n+ y\n", displayMode: true },
  ]);
  assert.deepEqual(
    [...root.querySelectorAll("p")].map((p) => p.textContent),
    ["Before", "After"],
    "display math interrupts paragraphs and can contain blank lines",
  );
}
assertMath("> \\[\n> x + y\n> \\]", [{ text: "\nx + y\n", displayMode: true }]);
assertMath("- \\(x\\)\n- \\[y\\]", [
  { text: "x", displayMode: false },
  { text: "y", displayMode: true },
]);

// An escaped backslash must not open or close a math expression.
for (const source of [
  String.raw`\\(x\\)`,
  String.raw`\\[x\\]`,
  String.raw`Before \\(x\\) after.`,
  String.raw`Before \\[x\\] after.`,
  String.raw`\$x\$`,
  String.raw`\(literal)`,
  String.raw`\[literal]`,
]) {
  assertMath(source, []);
}
assertMath(String.raw`\\\(x\)`, [{ text: "x", displayMode: false }]);
assertMath(String.raw`Before \\\[x\] after.`, [{ text: "x", displayMode: true }]);
assertMath(String.raw`\(x\\) + y\)`, [{ text: String.raw`x\\) + y`, displayMode: false }]);
assertMath(String.raw`\[x\\] + y\]`, [{ text: String.raw`x\\] + y`, displayMode: true }]);
assertMath(String.raw`\(x\\\)`, [{ text: String.raw`x\\`, displayMode: false }]);
assertMath(String.raw`\[x\\\]`, [{ text: String.raw`x\\`, displayMode: true }]);
assertMath(String.raw`\(x \] + y\)`, [{ text: String.raw`x \] + y`, displayMode: false }]);
assertMath(String.raw`\[x \) + y\]`, [{ text: String.raw`x \) + y`, displayMode: true }]);
assertMath(String.raw`\(x + $y$ + z\)`, [{ text: "x + $y$ + z", displayMode: false }]);

for (const source of [
  String.raw`\(x`,
  String.raw`\[x`,
  String.raw`\(x\]`,
  String.raw`\[x\)`,
  "$$x",
  "Costs $5 and $10.",
]) {
  assertMath(source, []);
}
assert.equal(
  parseMath(String.raw`\[x_$y$`).root.textContent?.trim(),
  String.raw`\[x_$y$`,
  "an unfinished display expression stays literal while streaming",
);
{
  const partial = String.raw`\[x_$y$ **bold** <img src=x onerror=alert(1)>` + "\\";
  const { root } = assertMath(partial, []);
  assert.equal(root.textContent?.trim(), partial);
  assert.equal(root.querySelector("img, strong"), null);
  assertMath(partial + "]", [{ text: partial.slice(2, -1), displayMode: true }]);
}

// Math text is escaped before sanitization; KaTeX retains its untrusted policy.
for (const [open, close] of [
  [String.raw`\(`, String.raw`\)`],
  [String.raw`\[`, String.raw`\]`],
]) {
  for (const text of [
    "<img src=x onerror=alert(1)>",
    "</span><script>alert(1)</script>",
    String.raw`\href{javascript:alert(1)}{x}`,
    String.raw`\htmlClass{injected}{x}`,
  ]) {
    const { root, nodes } = assertMath(`${open}${text}${close}`, [
      { text, displayMode: open === String.raw`\[` },
    ]);
    assert.equal(root.querySelector("img, script, a, .injected"), null);
    renderMath(nodes);
    assert.equal(root.querySelector("img, script, a, .injected"), null);
  }
}

console.log("Markdown math tests passed");
