// The Shelley window's half of the bookmarklet protocol: whatever a page
// sends is clipped to known shapes, and becomes plain composer text.
import {
  HTML_LIMIT,
  TAG,
  pageText,
  parsePageContext,
  parsePageMessage,
  pickText,
} from "./protocol";

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

run("accepts only web pages as context", () => {
  assertEqual(
    parsePageContext({ url: "https://example.com/a", title: " Docs\n home ", selection: "x" }),
    { url: "https://example.com/a", title: "Docs home", selection: "x" },
    "web page",
  );
  assertEqual(
    parsePageContext({ url: "javascript:alert(1)", title: "", selection: "" }),
    null,
    "js",
  );
  assertEqual(parsePageContext("https://example.com"), null, "not an object");
  assertEqual(parsePageContext({ url: "http://[", title: "", selection: "" }), null, "malformed");
  assertEqual(
    parsePageContext({ url: "https://Example.com/a b\n", title: "", selection: "" })?.url,
    "https://example.com/a%20b",
    "normalized",
  );
  assertEqual(
    parsePageContext({
      url: `https://example.com/dash?q=${"x".repeat(3000)}#top`,
      title: "",
      selection: "",
    })?.url,
    "https://example.com/dash",
    "long query dropped",
  );
  assertEqual(
    parsePageContext({ url: `https://example.com/${"x".repeat(3000)}`, title: "", selection: "" }),
    null,
    "long path",
  );
});

run("keeps only known message shapes", () => {
  assertEqual(parsePageMessage({ tag: "other", type: "hi" }), null, "foreign tag");
  assertEqual(
    parsePageMessage({ tag: TAG, type: "record", mode: "screen", send: true }),
    { tag: TAG, type: "record" },
    "record drops extra fields",
  );
  assertEqual(
    parsePageMessage({ tag: TAG, type: "run", command: "rm -rf /" }),
    null,
    "unknown type",
  );
  assertEqual(
    parsePageMessage({ tag: TAG, type: "trace", id: 1, epoch: "now" }),
    null,
    "bad epoch",
  );
  assertEqual(
    parsePageMessage({ tag: TAG, type: "trace", id: 2, epoch: 5 }),
    { tag: TAG, type: "trace", id: 2, epoch: 5 },
    "trace",
  );
  assertEqual(
    parsePageMessage({ tag: TAG, type: "event", event: { ms: 1, type: "exec" } }),
    null,
    "unknown event type",
  );
  assertEqual(
    parsePageMessage({
      tag: TAG,
      type: "event",
      event: { ms: 5, type: "click", where: "main > a", extra: { deep: true }, items: [1, "h"] },
    }),
    { tag: TAG, type: "event", event: { ms: 5, type: "click", where: "main > a", items: ["h"] } },
    "event fields are whitelisted",
  );
  const pick = parsePageMessage({
    tag: TAG,
    type: "pick",
    pick: { url: "https://example.com/", selector: "main > a", html: "x".repeat(HTML_LIMIT + 50) },
  });
  assertEqual(pick?.type === "pick" && pick.pick.html.length, HTML_LIMIT, "html is clipped");
  const multiline = parsePageMessage({
    tag: TAG,
    type: "pick",
    pick: { url: "https://example.com/", selector: "main\n> a", html: "" },
  });
  assertEqual(
    multiline?.type === "pick" && multiline.pick.selector,
    "main > a",
    "one-line selector",
  );
});

run("renders composer text", () => {
  assertEqual(
    pageText({ url: "https://example.com/", title: "Example", selection: "one\ntwo" }),
    "Page: Example — https://example.com/\n> one\n> two",
    "page with selection",
  );
  assertEqual(
    pageText({ url: "https://example.com/", title: "", selection: " " }),
    "Page: https://example.com/",
    "untitled page",
  );
  assertEqual(
    pickText({ url: "https://example.com/", selector: "main > pre", html: "<pre>```</pre>" }),
    "Picked on https://example.com/: `main > pre`\n````html\n<pre>```</pre>\n````",
    "fence outgrows the content's backticks",
  );
});
