// Coalesce adjacent text content blocks into a single renderable unit.
//
// Why: when a model uses server-side web search (Anthropic), its answer comes
// back as MANY separate text content blocks — normal prose interleaved with
// short "cited" quotes (each quote is its own block carrying a `Citations`
// array). The chat UI used to wrap every block in its own <div> and render
// each as standalone markdown, so a single sentence interrupted by a citation
// got chopped into several visual lines (see the "tree-structured sessions…"
// artifact). Merging consecutive text blocks back into one string fixes the
// stray line breaks AND gives us a single place to render inline source
// markers for the citations.
//
// This module is framework-agnostic so the React component (Message.tsx) and
// the Vue SFC (Message.vue / MessageContentBlock.vue) share identical logic.
import type { LLMContent } from "../types";

export interface Citation {
  // Message-global, 1-based citation number. The same number is used by the
  // inline <sup> marker and by the Sources list entry, so they always agree
  // even across multiple text runs in one message.
  num: number;
  url: string;
  title: string;
  citedText: string;
}

// A coalesced item is either a merged run of adjacent text blocks or a single
// passthrough non-text block (tool calls, thinking, web-search widgets, …).
export interface CoalescedItem {
  kind: "text" | "other";
  // For "text": the raw concatenation of the run (used by raw / markdown-off
  // mode, rendered through InlineText).
  text: string;
  // For "text": the same concatenation but with inline citation markers
  // injected as HTML <sup> source links (used by markdown mode).
  markdownText: string;
  // For "text": the de-duplicated, numbered list of cited sources, in the
  // order they were first referenced. Empty when there are no citations.
  citations: Citation[];
  // Provider-supplied Search Suggestions HTML, kept in order across blocks.
  googleSearchSuggestions: string[];
  // For "other": the original block to dispatch on.
  content?: LLMContent;
}

export interface ContentEntity {
  key: string;
  kind: "thinking" | "content";
  items: CoalescedItem[];
  copyText: string;
}

function entityText(item: CoalescedItem): string {
  if (item.kind === "text") return item.text;
  return item.content?.Thinking || item.content?.Text || "";
}

// Split one rendered message into independently actionable content regions
// without changing the message's outer DOM identity. Each thinking block stands
// alone; adjacent non-thinking content remains grouped.
export function splitContentEntities(items: CoalescedItem[]): ContentEntity[] {
  const entities: ContentEntity[] = [];
  for (const item of items) {
    const kind = item.kind === "other" && item.content?.Type === 3 ? "thinking" : "content";
    const last = entities.at(-1);
    if (kind === "content" && last?.kind === "content") {
      last.items.push(item);
    } else {
      entities.push({ key: `${kind}-${entities.length}`, kind, items: [item], copyText: "" });
    }
  }
  for (const entity of entities) {
    entity.copyText = entity.items.map(entityText).filter(Boolean).join("\n");
  }
  return entities;
}

// Anthropic citation wire shape (a text block's Citations is an array of these).
interface RawCitation {
  type?: string;
  url?: string;
  title?: string;
  cited_text?: string;
  end_utf16?: number;
}

function parseCitations(raw: unknown): RawCitation[] {
  if (!raw) return [];
  let arr: unknown = raw;
  if (typeof raw === "string") {
    const s = raw.trim();
    if (s === "" || s === "null") return [];
    try {
      arr = JSON.parse(s);
    } catch {
      return [];
    }
  }
  if (!Array.isArray(arr)) return [];
  return arr.filter((c): c is RawCitation => !!c && typeof c === "object");
}

// Escape a string for safe inclusion in an HTML attribute. The result is still
// run through DOMPurify downstream; this just prevents attribute breakout in
// the marker we synthesize.
function escapeAttr(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/"/g, "&quot;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

// Build the inline marker HTML for a set of citations, e.g.
//   <sup class="citation-refs"><a class="citation-ref" href=… >1</a>…</sup>
function markerHTML(refs: Citation[]): string {
  if (refs.length === 0) return "";
  const links = refs
    .map((cit) => {
      const title = escapeAttr(cit.title || cit.url);
      const href = escapeAttr(cit.url);
      return `<a class="citation-ref" href="${href}" title="${title}" target="_blank" rel="noopener noreferrer">${cit.num}</a>`;
    })
    .join("");
  return `<sup class="citation-refs">${links}</sup>`;
}

// coalesceContent walks a message's content blocks and merges runs of adjacent
// text blocks (Type === 2) into a single item, leaving every other block as a
// standalone passthrough item in its original position.
export function coalesceContent(blocks: LLMContent[]): CoalescedItem[] {
  const items: CoalescedItem[] = [];
  // Citation numbering is per-message and de-duplicated by URL, so a source
  // reused anywhere in the message keeps one stable number. The number is
  // carried on the Citation itself, so inline markers and Sources-list entries
  // always show the same value — even when a message has multiple text runs
  // (e.g. a second web search interrupts the prose).
  const citationByURL = new Map<string, Citation>();

  let runRaw: string[] = [];
  let runMd: string[] = [];
  let runCites: Citation[] = [];
  let runSuggestions: string[] = [];

  const flush = () => {
    if (runRaw.length === 0) return;
    items.push({
      kind: "text",
      text: runRaw.join(""),
      markdownText: runMd.join(""),
      citations: runCites,
      googleSearchSuggestions: runSuggestions,
    });
    runRaw = [];
    runMd = [];
    runCites = [];
    runSuggestions = [];
  };

  for (const block of blocks) {
    if (block.Type === 2) {
      const text = block.Text || "";
      runRaw.push(text);

      if (block.GoogleSearchSuggestions) runSuggestions.push(block.GoogleSearchSuggestions);
      const cites = parseCitations(block.Citations);
      const byEnd = new Map<number, RawCitation[]>();
      for (const rc of cites) {
        const end =
          rc.type === "google_grounding" && rc.end_utf16 !== undefined ? rc.end_utf16 : text.length;
        const group = byEnd.get(end) ?? [];
        group.push(rc);
        byEnd.set(end, group);
      }
      let start = 0;
      for (const [end, group] of [...byEnd].sort(([a], [b]) => a - b)) {
        runMd.push(text.slice(start, end));
        start = end;
        const refs: Citation[] = [];
        for (const rc of group) {
          const url = rc.url || "";
          if (!url) continue;
          let cit = citationByURL.get(url);
          if (cit === undefined) {
            cit = {
              num: citationByURL.size + 1,
              url,
              title: rc.title || url,
              citedText: rc.cited_text || "",
            };
            citationByURL.set(url, cit);
            // Surface each source in the run where it is first referenced.
            runCites.push(cit);
          }
          if (!refs.includes(cit)) refs.push(cit);
        }
        // Citation HTML on a closing fence's line would make it part of the code.
        const lastLine = text.slice(text.lastIndexOf("\n", end - 1) + 1, end);
        if (refs.length > 0 && /^ {0,3}(?:`{3,}|~{3,})[ \t]*$/.test(lastLine)) {
          runMd.push("\n");
        }
        runMd.push(markerHTML(refs));
      }
      runMd.push(text.slice(start));
    } else {
      flush();
      items.push({
        kind: "other",
        text: "",
        markdownText: "",
        citations: [],
        googleSearchSuggestions: [],
        content: block,
      });
    }
  }
  flush();
  return items;
}
