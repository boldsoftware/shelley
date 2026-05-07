import { parseInlineSegments, type InlineSegment } from "./inlineText";

interface TestCase {
  name: string;
  input: string;
  expected: InlineSegment[];
}

const testCases: TestCase[] = [
  {
    name: "plain text",
    input: "Hello world",
    expected: [{ type: "text", content: "Hello world" }],
  },
  {
    name: "single inline code",
    input: "press `Enter` now",
    expected: [
      { type: "text", content: "press " },
      { type: "code", content: "Enter" },
      { type: "text", content: " now" },
    ],
  },
  {
    name: "multiple inline codes on one line",
    input: "use `foo` and `bar`",
    expected: [
      { type: "text", content: "use " },
      { type: "code", content: "foo" },
      { type: "text", content: " and " },
      { type: "code", content: "bar" },
    ],
  },
  {
    name: "fenced code block",
    input: "before\n```\nline1\nline2\n```\nafter",
    expected: [
      { type: "text", content: "before\n" },
      { type: "codeblock", content: "line1\nline2" },
      { type: "text", content: "\nafter" },
    ],
  },
  {
    name: "fenced block with language hint",
    input: "```js\nconsole.log(1)\n```",
    expected: [{ type: "codeblock", content: "console.log(1)" }],
  },
  {
    name: "fenced block inline on one line",
    input: "```code```",
    expected: [{ type: "codeblock", content: "code" }],
  },
  {
    name: "unclosed single backtick is literal",
    input: "a `b c",
    expected: [{ type: "text", content: "a `b c" }],
  },
  {
    name: "empty inline code is literal",
    input: "a `` b",
    expected: [{ type: "text", content: "a `` b" }],
  },
  {
    name: "inline code does not cross newlines",
    input: "a `b\nc` d",
    expected: [{ type: "text", content: "a `b\nc` d" }],
  },
  {
    name: "mixed inline and block",
    input: "use `foo`\n```\nblock\n```\nthen `bar`",
    expected: [
      { type: "text", content: "use " },
      { type: "code", content: "foo" },
      { type: "text", content: "\n" },
      { type: "codeblock", content: "block" },
      { type: "text", content: "\nthen " },
      { type: "code", content: "bar" },
    ],
  },
  {
    name: "backticks only text unchanged",
    input: "no code here",
    expected: [{ type: "text", content: "no code here" }],
  },
];

function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (typeof a !== typeof b) return false;
  if (typeof a !== "object" || a === null || b === null) return false;
  if (Array.isArray(a) && Array.isArray(b)) {
    if (a.length !== b.length) return false;
    return a.every((v, i) => deepEqual(v, b[i]));
  }
  if (Array.isArray(a) || Array.isArray(b)) return false;
  const ao = a as Record<string, unknown>;
  const bo = b as Record<string, unknown>;
  const ak = Object.keys(ao);
  const bk = Object.keys(bo);
  if (ak.length !== bk.length) return false;
  return ak.every((k) => deepEqual(ao[k], bo[k]));
}

export function runTests(): { passed: number; failed: number; failures: string[] } {
  let passed = 0;
  let failed = 0;
  const failures: string[] = [];
  for (const tc of testCases) {
    const result = parseInlineSegments(tc.input);
    if (deepEqual(result, tc.expected)) {
      passed++;
    } else {
      failed++;
      failures.push(
        `FAIL: ${tc.name}\n  Input: ${JSON.stringify(tc.input)}\n  Expected: ${JSON.stringify(tc.expected)}\n  Got: ${JSON.stringify(result)}`,
      );
    }
  }
  return { passed, failed, failures };
}

export { testCases };
