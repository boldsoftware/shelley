// Unit tests for coalesceContent.
// Run with: tsx src/utils/coalesceContent.test.ts
import { coalesceContent, splitContentEntities } from "./coalesceContent";
import type { LLMContent } from "../types";

let passed = 0;
let failed = 0;
const failures: string[] = [];
function check(name: string, cond: boolean, detail?: unknown) {
  if (cond) {
    passed++;
  } else {
    failed++;
    failures.push(`✗ ${name}${detail !== undefined ? `\n   ${JSON.stringify(detail)}` : ""}`);
  }
}

function text(t: string, citations?: unknown): LLMContent {
  return { ID: "", Type: 2, Text: t, Citations: citations } as unknown as LLMContent;
}
function thinking(t: string): LLMContent {
  return { ID: "", Type: 3, Thinking: t } as LLMContent;
}

function tool(): LLMContent {
  return { ID: "x", Type: 5, ToolName: "bash" } as unknown as LLMContent;
}

// --- Adjacent text blocks merge into one item (the core bug fix) ---
{
  const items = coalesceContent([
    text("\n\nOne nice bonus tied to switching: Pi keeps "),
    text("tree-structured sessions — never lose work"),
    text(", so model switching pairs well with rewinding."),
  ]);
  check("single merged text item", items.length === 1, items.length);
  check(
    "merged text is the full sentence",
    items[0].text ===
      "\n\nOne nice bonus tied to switching: Pi keeps tree-structured sessions — never lose work, so model switching pairs well with rewinding.",
    items[0].text,
  );
}

// --- Non-text blocks split runs and pass through ---
{
  const items = coalesceContent([text("before "), text("more"), tool(), text("after")]);
  check("three items (text, other, text)", items.length === 3, items.length);
  check("first merged", items[0].kind === "text" && items[0].text === "before more", items[0]);
  check("middle passthrough", items[1].kind === "other" && items[1].content?.Type === 5, items[1]);
  check("last text", items[2].kind === "text" && items[2].text === "after", items[2]);
}

// --- Citations produce inline <sup> markers + numbering ---
{
  const cites = [
    { type: "web_search_result_location", url: "https://a.example", title: "A", cited_text: "q" },
  ];
  const items = coalesceContent([text("Pi keeps "), text("good sessions", cites), text(" today.")]);
  check("one item", items.length === 1, items.length);
  const md = items[0].markdownText;
  check("marker present", md.includes('<sup class="citation-refs">'), md);
  check("marker links to source", md.includes('href="https://a.example"'), md);
  check("marker numbered 1", md.includes(">1</a>"), md);
  check("citation collected", items[0].citations.length === 1, items[0].citations);
  check("citation numbered 1", items[0].citations[0].num === 1, items[0].citations);
  check("raw text has no marker", !items[0].text.includes("<sup"), items[0].text);
}

// --- Multi-run: marker numbers and Sources numbers stay in sync ---
{
  // A non-text block (tool use) splits the text into two runs. A new source in
  // the second run must keep its global number in BOTH the inline marker and
  // its Sources entry.
  const a = [{ url: "https://a.example", title: "A", cited_text: "x" }];
  const b = [{ url: "https://b.example", title: "B", cited_text: "y" }];
  const items = coalesceContent([text("first", a), tool(), text("second", b)]);
  check("three items", items.length === 3, items.length);
  const run1 = items[0];
  const run2 = items[2];
  check("run1 source num 1", run1.kind === "text" && run1.citations[0].num === 1, run1);
  check("run2 source num 2", run2.kind === "text" && run2.citations[0].num === 2, run2);
  // The inline marker in run2 must read 2 (not restart at 1).
  check("run2 marker reads 2", run2.markdownText.includes(">2</a>"), run2.markdownText);
  check("run2 marker not 1", !run2.markdownText.includes(">1</a>"), run2.markdownText);
}

// --- Same URL reused keeps the same number ---
{
  const c1 = [{ url: "https://a.example", title: "A", cited_text: "x" }];
  const items = coalesceContent([text("one", c1), text(" two", c1)]);
  const md = items[0].markdownText;
  const ones = (md.match(/>1<\/a>/g) || []).length;
  check("reused url uses number 1 twice", ones === 2, md);
  check("no number 2 created", !md.includes(">2</a>"), md);
}

// --- Empty/null citations are ignored gracefully ---
{
  const items = coalesceContent([text("a", []), text("b", "null"), text("c", null)]);
  check("merged ignoring empty cites", items.length === 1 && items[0].text === "abc", items[0]);
  check("no markers", !items[0].markdownText.includes("<sup"), items[0].markdownText);
}

// --- Attribute escaping prevents breakout ---
{
  const evil = [{ url: 'https://x"onerror=alert(1)', title: '<img>"', cited_text: "q" }];
  const items = coalesceContent([text("hi", evil)]);
  const md = items[0].markdownText;
  check("quote escaped in href", !md.includes('"onerror'), md);
  check("angle escaped in title", !md.includes("<img>"), md);
}

// --- Thinking and answer content become independent action regions ---
{
  const entities = splitContentEntities(
    coalesceContent([
      thinking("first thought"),
      thinking("second thought"),
      text("answer before "),
      text("and after"),
      thinking("final thought"),
      text("final answer"),
    ]),
  );
  check(
    "thinking and answer entity order",
    entities.map((entity) => entity.kind).join(",") ===
      "thinking,thinking,content,thinking,content",
    entities,
  );
  check("first thinking copies alone", entities[0]?.copyText === "first thought", entities[0]);
  check("second thinking copies alone", entities[1]?.copyText === "second thought", entities[1]);
  check(
    "adjacent answer text stays grouped",
    entities[2]?.copyText === "answer before and after",
    entities[2],
  );
  check(
    "thinking-only input is one entity",
    splitContentEntities(coalesceContent([thinking("only")])).length === 1,
  );
  check(
    "answer-only input is one entity",
    splitContentEntities(coalesceContent([text("only")])).length === 1,
  );
}

console.log(`\ncoalesceContent Tests: ${passed} passed, ${failed} failed\n`);
if (failures.length > 0) {
  for (const f of failures) console.log(f);
  process.exit(1);
}
console.log("All tests passed!");
process.exit(0);
