import {
  parseLinks,
  rewriteLocalhostLink,
  type LinkifyResult,
  type LocalhostLinkOptions,
} from "./linkify";

interface TestCase {
  name: string;
  input: string;
  expected: LinkifyResult[];
  options?: LocalhostLinkOptions;
}

const exeDev = { isExeDev: true, hostname: "demo.exe.xyz" };

const testCases: TestCase[] = [
  {
    name: "plain text with no URLs",
    input: "Hello world",
    expected: [{ type: "text", content: "Hello world" }],
  },
  {
    name: "simple http URL",
    input: "Check out http://example.com for more",
    expected: [
      { type: "text", content: "Check out " },
      { type: "link", content: "http://example.com", href: "http://example.com" },
      { type: "text", content: " for more" },
    ],
  },
  {
    name: "simple https URL",
    input: "Visit https://example.com today",
    expected: [
      { type: "text", content: "Visit " },
      { type: "link", content: "https://example.com", href: "https://example.com" },
      { type: "text", content: " today" },
    ],
  },
  {
    name: "URL with path",
    input: "See https://example.com/path/to/page for details",
    expected: [
      { type: "text", content: "See " },
      {
        type: "link",
        content: "https://example.com/path/to/page",
        href: "https://example.com/path/to/page",
      },
      { type: "text", content: " for details" },
    ],
  },
  {
    name: "URL with query parameters",
    input: "Link: https://example.com/search?q=test&page=1",
    expected: [
      { type: "text", content: "Link: " },
      {
        type: "link",
        content: "https://example.com/search?q=test&page=1",
        href: "https://example.com/search?q=test&page=1",
      },
    ],
  },
  {
    name: "URL with port",
    input: "Server at https://localhost:8080/api",
    expected: [
      { type: "text", content: "Server at " },
      {
        type: "link",
        content: "https://localhost:8080/api",
        href: "https://localhost:8080/api",
      },
    ],
  },
  {
    name: "URL followed by period (sentence end)",
    input: "Check https://example.com.",
    expected: [
      { type: "text", content: "Check " },
      { type: "link", content: "https://example.com", href: "https://example.com" },
      { type: "text", content: "." },
    ],
  },
  {
    name: "URL followed by comma",
    input: "Visit https://example.com, then continue",
    expected: [
      { type: "text", content: "Visit " },
      { type: "link", content: "https://example.com", href: "https://example.com" },
      { type: "text", content: ", then continue" },
    ],
  },
  {
    name: "URL followed by exclamation",
    input: "Wow https://example.com!",
    expected: [
      { type: "text", content: "Wow " },
      { type: "link", content: "https://example.com", href: "https://example.com" },
      { type: "text", content: "!" },
    ],
  },
  {
    name: "URL followed by question mark",
    input: "Have you seen https://example.com?",
    expected: [
      { type: "text", content: "Have you seen " },
      { type: "link", content: "https://example.com", href: "https://example.com" },
      { type: "text", content: "?" },
    ],
  },
  {
    name: "multiple URLs",
    input: "Try https://a.com and https://b.com too",
    expected: [
      { type: "text", content: "Try " },
      { type: "link", content: "https://a.com", href: "https://a.com" },
      { type: "text", content: " and " },
      { type: "link", content: "https://b.com", href: "https://b.com" },
      { type: "text", content: " too" },
    ],
  },
  {
    name: "URL at start of text",
    input: "https://example.com is the site",
    expected: [
      { type: "link", content: "https://example.com", href: "https://example.com" },
      { type: "text", content: " is the site" },
    ],
  },
  {
    name: "URL at end of text",
    input: "The site is https://example.com",
    expected: [
      { type: "text", content: "The site is " },
      { type: "link", content: "https://example.com", href: "https://example.com" },
    ],
  },
  {
    name: "URL only",
    input: "https://example.com",
    expected: [{ type: "link", content: "https://example.com", href: "https://example.com" }],
  },
  {
    name: "empty string",
    input: "",
    expected: [],
  },
  {
    name: "URL with fragment",
    input: "See https://example.com/page#section for more",
    expected: [
      { type: "text", content: "See " },
      {
        type: "link",
        content: "https://example.com/page#section",
        href: "https://example.com/page#section",
      },
      { type: "text", content: " for more" },
    ],
  },
  {
    name: "URL in parentheses - should not include closing paren",
    input: "(see https://example.com)",
    expected: [
      { type: "text", content: "(see " },
      { type: "link", content: "https://example.com", href: "https://example.com" },
      { type: "text", content: ")" },
    ],
  },
  {
    name: "URL with trailing colon and more text",
    input: "URL: https://example.com: that was it",
    expected: [
      { type: "text", content: "URL: " },
      { type: "link", content: "https://example.com", href: "https://example.com" },
      { type: "text", content: ": that was it" },
    ],
  },
  {
    name: "does not match ftp URLs",
    input: "Not matched: ftp://example.com",
    expected: [{ type: "text", content: "Not matched: ftp://example.com" }],
  },
  {
    name: "does not match mailto",
    input: "Email: mailto:test@example.com",
    expected: [{ type: "text", content: "Email: mailto:test@example.com" }],
  },
  {
    name: "URL with underscores and dashes",
    input: "Go to https://my-site.example.com/some_page",
    expected: [
      { type: "text", content: "Go to " },
      {
        type: "link",
        content: "https://my-site.example.com/some_page",
        href: "https://my-site.example.com/some_page",
      },
    ],
  },
  {
    name: "URL followed by semicolon",
    input: "First https://a.com; then more",
    expected: [
      { type: "text", content: "First " },
      { type: "link", content: "https://a.com", href: "https://a.com" },
      { type: "text", content: "; then more" },
    ],
  },
  {
    name: "newlines around URL",
    input: "Line 1\nhttps://example.com\nLine 3",
    expected: [
      { type: "text", content: "Line 1\n" },
      { type: "link", content: "https://example.com", href: "https://example.com" },
      { type: "text", content: "\nLine 3" },
    ],
  },
  {
    name: "XSS attempt in URL - javascript protocol not matched",
    input: "javascript:alert('xss')",
    expected: [{ type: "text", content: "javascript:alert('xss')" }],
  },
  {
    name: "XSS attempt - script tags in text preserved as text",
    input: "<script>alert('xss')</script> https://example.com",
    expected: [
      { type: "text", content: "<script>alert('xss')</script> " },
      { type: "link", content: "https://example.com", href: "https://example.com" },
    ],
  },
  {
    name: "URL not matched inside angle brackets",
    input: "See <https://example.com> for more",
    expected: [
      { type: "text", content: "See <" },
      { type: "link", content: "https://example.com", href: "https://example.com" },
      { type: "text", content: "> for more" },
    ],
  },
  {
    name: "URL in markdown bold - should not include asterisks",
    input: "Download here: **https://example.com/file.vsix**",
    expected: [
      { type: "text", content: "Download here: **" },
      {
        type: "link",
        content: "https://example.com/file.vsix",
        href: "https://example.com/file.vsix",
      },
      { type: "text", content: "**" },
    ],
  },
  {
    name: "rewrites only the href of a plain-text localhost link when opted in",
    input: "Open http://localhost:3000/app?q=one#result",
    options: exeDev,
    expected: [
      { type: "text", content: "Open " },
      {
        type: "link",
        content: "http://localhost:3000/app?q=one#result",
        href: "https://demo.exe.xyz:3000/app?q=one#result",
      },
    ],
  },
  {
    name: "keeps a localhost URL inside a shell command literal",
    input: `ssh vm "curl -fsS -d '{}' http://localhost:9999/feature-flags" && echo ok`,
    options: exeDev,
    expected: [
      { type: "text", content: `ssh vm "curl -fsS -d '{}' ` },
      {
        type: "link",
        content: "http://localhost:9999/feature-flags",
        href: "https://demo.exe.xyz:9999/feature-flags",
      },
      { type: "text", content: `" && echo ok` },
    ],
  },
  {
    name: "does not duplicate a malformed URL when rewriting is enabled",
    input: "Broken http://localhost:bad/path once",
    options: exeDev,
    expected: [
      { type: "text", content: "Broken " },
      {
        type: "link",
        content: "http://localhost:bad/path",
        href: "http://localhost:bad/path",
      },
      { type: "text", content: " once" },
    ],
  },
];

const rewriteTestCases: Array<{
  name: string;
  input: string;
  options: LocalhostLinkOptions;
  expected: string;
}> = [
  {
    name: "rewrites localhost with path, query, and hash",
    input: "http://localhost:3000/a/b?q=one#two",
    options: exeDev,
    expected: "https://demo.exe.xyz:3000/a/b?q=one#two",
  },
  {
    name: "rewrites the upper port boundary",
    input: "https://0.0.0.0:9999/",
    options: exeDev,
    expected: "https://demo.exe.xyz:9999/",
  },
  {
    name: "rewrites loopback IPv4",
    input: "http://127.0.0.1:4321/path",
    options: exeDev,
    expected: "https://demo.exe.xyz:4321/path",
  },
  {
    name: "matches localhost case-insensitively",
    input: "http://LOCALHOST:3000/path",
    options: exeDev,
    expected: "https://demo.exe.xyz:3000/path",
  },
  {
    name: "does nothing outside exe.dev",
    input: "http://localhost:3000/path",
    options: { ...exeDev, isExeDev: false },
    expected: "http://localhost:3000/path",
  },
  {
    name: "does nothing without an injected hostname",
    input: "http://localhost:3000/path",
    options: { ...exeDev, hostname: "" },
    expected: "http://localhost:3000/path",
  },
  {
    name: "does nothing without an explicit port",
    input: "http://localhost/path",
    options: exeDev,
    expected: "http://localhost/path",
  },
  {
    name: "does nothing below the proxy port range",
    input: "http://localhost:2999/path",
    options: exeDev,
    expected: "http://localhost:2999/path",
  },
  {
    name: "does nothing above the proxy port range",
    input: "http://localhost:10000/path",
    options: exeDev,
    expected: "http://localhost:10000/path",
  },
  {
    name: "does nothing for credentials",
    input: "http://user:pass@localhost:3000/path",
    options: exeDev,
    expected: "http://user:pass@localhost:3000/path",
  },
  {
    name: "does nothing for a localhost subdomain",
    input: "http://app.localhost:3000/path",
    options: exeDev,
    expected: "http://app.localhost:3000/path",
  },

  {
    name: "does nothing for another protocol",
    input: "ftp://localhost:3000/path",
    options: exeDev,
    expected: "ftp://localhost:3000/path",
  },
  {
    name: "does nothing for malformed URLs",
    input: "http://localhost:bad/path",
    options: exeDev,
    expected: "http://localhost:bad/path",
  },
];

function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (typeof a !== typeof b) return false;
  if (a === null || b === null) return a === b;
  if (typeof a !== "object") return false;

  if (Array.isArray(a) && Array.isArray(b)) {
    if (a.length !== b.length) return false;
    return a.every((item, i) => deepEqual(item, b[i]));
  }

  if (Array.isArray(a) || Array.isArray(b)) return false;

  const aObj = a as Record<string, unknown>;
  const bObj = b as Record<string, unknown>;
  const aKeys = Object.keys(aObj);
  const bKeys = Object.keys(bObj);

  if (aKeys.length !== bKeys.length) return false;
  return aKeys.every((key) => deepEqual(aObj[key], bObj[key]));
}

export function runTests(): { passed: number; failed: number; failures: string[] } {
  let passed = 0;
  let failed = 0;
  const failures: string[] = [];

  for (const tc of testCases) {
    const result = parseLinks(tc.input, tc.options);
    if (deepEqual(result, tc.expected)) {
      passed++;
    } else {
      failed++;
      failures.push(
        `FAIL: ${tc.name}\n  Input: ${JSON.stringify(tc.input)}\n  Expected: ${JSON.stringify(tc.expected)}\n  Got: ${JSON.stringify(result)}`,
      );
    }
  }

  for (const tc of rewriteTestCases) {
    const result = rewriteLocalhostLink(tc.input, tc.options);
    if (result === tc.expected) {
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

// Export test cases for use in browser
export { testCases };
