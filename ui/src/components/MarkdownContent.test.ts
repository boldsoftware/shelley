// Unit tests for markdown sanitization.
// Run with: tsx src/components/MarkdownContent.test.ts

import { JSDOM } from "jsdom";

// Provide a DOM environment so DOMPurify (which MarkdownContent imports) can
// run under tsx/node. Must be set up before importing the module under test.
const dom = new JSDOM("");
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const g = globalThis as any;
g.window = dom.window;
g.document = dom.window.document;
// Newer Node versions expose a read-only `navigator` global; only set it when
// it is writable so DOMPurify can find one without crashing under CI's Node.
if (!g.navigator) {
  try {
    g.navigator = dom.window.navigator;
  } catch {
    // ignore: a built-in read-only navigator is already present.
  }
}

const { renderMarkdownToSafeHTML, classifyImageSrc } = await import("./MarkdownContent");

// Default message id used so local images get rewritten/rendered in tests.
const MSG = "msg-test";

function renderMarkdown(text: string): string {
  return renderMarkdownToSafeHTML(text, MSG);
}

interface TestCase {
  name: string;
  input: string;
  // When set, render without a messageId (local images cannot be authorized).
  noMessageId?: boolean;
  // A substring that MUST appear in the output.
  mustContain?: string[];
  // A substring that must NOT appear in the output.
  mustNotContain?: string[];
}

const testCases: TestCase[] = [
  // ---- XSS vectors ----
  {
    name: "script tag is stripped",
    input: "<script>alert('xss')</script>hello",
    mustContain: ["hello"],
    mustNotContain: ["<script", "alert"],
  },
  {
    name: "img tag with onerror is stripped",
    input: "<img src=x onerror=alert(1)>",
    mustNotContain: ["<img", "onerror", "alert"],
  },
  {
    name: "svg onload is stripped",
    input: "<svg onload=alert(1)>",
    mustNotContain: ["<svg", "onload", "alert"],
  },
  {
    name: "iframe is stripped",
    input: '<iframe src="https://evil.com"></iframe>hello',
    mustContain: ["hello"],
    mustNotContain: ["<iframe", "evil.com"],
  },
  {
    name: "event handler attributes are stripped",
    input: "<div onclick=alert(1)>click me</div>",
    mustContain: ["click me"],
    mustNotContain: ["onclick", "alert"],
  },
  {
    name: "javascript: href is sanitized",
    input: '<a href="javascript:alert(1)">click</a>',
    mustContain: ["click"],
    mustNotContain: ["javascript:"],
  },
  {
    name: "data: href is sanitized",
    input: '<a href="data:text/html,<script>alert(1)</script>">click</a>',
    mustContain: ["click"],
    mustNotContain: ["data:text/html"],
  },
  {
    name: "style attribute is stripped",
    input: '<div style="background:url(javascript:alert(1))">test</div>',
    mustContain: ["test"],
    mustNotContain: ["style="],
  },
  {
    name: "nested script in markdown",
    input: "**bold** <script>alert('xss')</script> *italic*",
    mustContain: ["<strong>bold</strong>", "<em>italic</em>"],
    mustNotContain: ["<script", "alert"],
  },
  {
    name: "remote markdown image is dropped (no tracking pixels)",
    input: "![alt text](https://evil.com/tracker.png)",
    mustNotContain: ["<img", "tracker.png"],
  },
  {
    name: "protocol-relative image is dropped",
    input: "![x](//evil.com/a.png)",
    mustNotContain: ["<img", "evil.com"],
  },
  {
    name: "local relative image is rewritten to file endpoint",
    input: "![chart](./out/chart.png)",
    mustContain: ["<img", "/api/message/msg-test/file?path=", "chart.png"],
    mustNotContain: ["onerror"],
  },
  {
    name: "local absolute image is rewritten to file endpoint",
    input: "![x](/tmp/work/a.png)",
    mustContain: ["<img", "/api/message/msg-test/file?path="],
  },
  {
    name: "local image without messageId is dropped",
    input: "![chart](./out/chart.png)",
    noMessageId: true,
    mustNotContain: ["<img"],
  },
  {
    name: "small inline image data URI is kept",
    input:
      "![dot](data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==)",
    mustContain: ["<img", "data:image/png;base64"],
  },
  {
    name: "non-image data URI is dropped",
    input: "![x](data:text/html;base64,PHNjcmlwdD4=)",
    mustNotContain: ["<img", "data:text/html"],
  },
  {
    name: "raw img tag with file endpoint path is still dropped if onerror present",
    input: '<img src="x" onerror="alert(1)">',
    mustNotContain: ["<img", "onerror", "alert"],
  },
  {
    name: "object tag is stripped",
    input: '<object data="evil.swf"></object>test',
    mustContain: ["test"],
    mustNotContain: ["<object", "evil.swf"],
  },
  {
    name: "embed tag is stripped",
    input: '<embed src="evil.swf">test',
    mustContain: ["test"],
    mustNotContain: ["<embed", "evil.swf"],
  },
  {
    name: "form tag is stripped",
    input: '<form action="https://evil.com"><input type="submit"></form>',
    mustNotContain: ["<form", "action", "evil.com"],
  },
  {
    name: "base tag is stripped",
    input: '<base href="https://evil.com">',
    mustNotContain: ["<base", "evil.com"],
  },
  {
    name: "meta refresh is stripped",
    input: '<meta http-equiv="refresh" content="0;url=https://evil.com">',
    mustNotContain: ["<meta", "refresh", "evil.com"],
  },

  // ---- Markdown rendering ----
  {
    name: "bold renders correctly",
    input: "**hello**",
    mustContain: ["<strong>hello</strong>"],
  },
  {
    name: "italic renders correctly",
    input: "*world*",
    mustContain: ["<em>world</em>"],
  },
  {
    name: "inline code renders correctly",
    input: "`code here`",
    mustContain: ["<code>code here</code>"],
  },
  {
    name: "heading renders correctly",
    input: "# Title",
    mustContain: ["<h1>Title</h1>"],
  },
  {
    name: "link renders correctly with target=_blank",
    input: "[click](https://example.com)",
    mustContain: ['href="https://example.com"', 'target="_blank"', 'rel="noopener noreferrer"'],
  },
  {
    name: "unordered list renders correctly",
    input: "- item 1\n- item 2",
    mustContain: ["<ul>", "<li>item 1</li>", "<li>item 2</li>"],
  },
  {
    name: "code block renders correctly",
    input: "```\nconst x = 1;\n```",
    mustContain: ["<pre>", "<code>", "const x = 1;"],
  },
  {
    name: "blockquote renders correctly",
    input: "> quoted text",
    mustContain: ["<blockquote>", "quoted text"],
  },
  {
    name: "table renders correctly",
    input: "| A | B |\n|---|---|\n| 1 | 2 |",
    mustContain: ["<table>", "<th>A</th>", "<td>1</td>"],
  },
  {
    name: "strikethrough renders correctly",
    input: "~~deleted~~",
    mustContain: ["<del>deleted</del>"],
  },

  // ---- Input restriction ----
  {
    name: "text input is stripped (phishing prevention)",
    input: '<input type="text" placeholder="Enter password">',
    mustNotContain: ["<input", 'type="text"', "Enter password"],
  },
  {
    name: "password input is stripped",
    input: '<input type="password">',
    mustNotContain: ["<input", 'type="password"'],
  },
  {
    name: "checkbox input is allowed (GFM task lists)",
    input: "- [x] done\n- [ ] todo",
    mustContain: ['type="checkbox"'],
  },

  // ---- Edge cases ----
  {
    name: "HTML entities in markdown are safe",
    input: "Use `<script>` tags carefully",
    mustContain: ["&lt;script&gt;"],
    mustNotContain: ["<script>"],
  },
  {
    name: "mixed markdown and HTML injection",
    input: "# Hello <img src=x onerror=alert(1)>\n\nSafe **bold** text",
    mustContain: ["<h1>", "Hello", "<strong>bold</strong>"],
    mustNotContain: ["<img", "onerror", "alert"],
  },
  {
    name: "SVG with embedded script is stripped",
    input: "<svg><script>alert(1)</script></svg>",
    mustNotContain: ["<svg", "<script", "alert"],
  },
  {
    name: "markdown link with javascript protocol",
    input: "[click me](javascript:alert(document.cookie))",
    mustContain: ["click me"],
    mustNotContain: ["javascript:"],
  },
  {
    name: "empty input returns empty or whitespace",
    input: "",
    mustNotContain: ["<script", "<img"],
  },
];

function runTests(): { passed: number; failed: number; failures: string[] } {
  let passed = 0;
  let failed = 0;
  const failures: string[] = [];

  for (const tc of testCases) {
    const output = tc.noMessageId ? renderMarkdownToSafeHTML(tc.input) : renderMarkdown(tc.input);
    let ok = true;
    const problems: string[] = [];

    for (const s of tc.mustContain ?? []) {
      if (!output.includes(s)) {
        ok = false;
        problems.push(`expected to contain: ${JSON.stringify(s)}`);
      }
    }
    for (const s of tc.mustNotContain ?? []) {
      if (output.includes(s)) {
        ok = false;
        problems.push(`must NOT contain: ${JSON.stringify(s)}`);
      }
    }

    if (ok) {
      passed++;
    } else {
      failed++;
      failures.push(
        `FAIL: ${tc.name}\n  Input:  ${JSON.stringify(tc.input)}\n  Output: ${JSON.stringify(output)}\n  ${problems.join("\n  ")}`,
      );
    }
  }

  return { passed, failed, failures };
}

// Export for potential future use by a generic runner.
export { testCases, runTests };

// ---- classifyImageSrc unit checks ----
function runClassifyTests(): string[] {
  const cases: Array<[string, string]> = [
    ["./out/x.png", "local"],
    ["out/x.png", "local"],
    ["../shared/x.png", "local"],
    ["/abs/x.png", "local"],
    ["https://e.com/x.png", "remote"],
    ["http://e.com/x.png", "remote"],
    ["//e.com/x.png", "remote"],
    ["file:///etc/passwd", "remote"],
    ["data:image/png;base64,AAAA", "data"],
    ["data:text/html,<b>", "invalid"],
    ["", "invalid"],
  ];
  const problems: string[] = [];
  for (const [src, want] of cases) {
    const got = classifyImageSrc(src);
    if (got !== want) {
      problems.push(`classifyImageSrc(${JSON.stringify(src)}) = ${got}, want ${want}`);
    }
  }
  return problems;
}

// ---- Self-running ----
const classifyProblems = runClassifyTests();
if (classifyProblems.length > 0) {
  console.log("classifyImageSrc failures:");
  for (const p of classifyProblems) console.log("  " + p);
  process.exit(1);
}

const { passed, failed, failures } = runTests();

console.log(`\nMarkdown Sanitization Tests: ${passed} passed, ${failed} failed\n`);

if (failures.length > 0) {
  console.log("Failures:");
  for (const f of failures) {
    console.log(f);
    console.log("");
  }
  process.exit(1);
}

console.log("All tests passed!");
process.exit(0);
