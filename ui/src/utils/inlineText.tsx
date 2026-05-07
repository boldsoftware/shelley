import React from "react";
import { parseLinks } from "./linkify";

export type InlineSegment =
  | { type: "text"; content: string }
  | { type: "code"; content: string }
  | { type: "codeblock"; content: string };

const FENCE = /```([\s\S]*?)```/g;
const INLINE_CODE = /`([^`\n]+)`/g;

/**
 * Parse text into fenced code blocks, inline code spans, and plain text.
 * Fenced blocks take precedence over inline code. Order is preserved.
 * If a fenced block's first line has no whitespace, it is treated as a
 * language hint and stripped (e.g. "```js\ncode\n```" -> "code\n").
 */
export function parseInlineSegments(text: string): InlineSegment[] {
  const out: InlineSegment[] = [];
  let last = 0;
  FENCE.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = FENCE.exec(text)) !== null) {
    if (m.index > last) {
      splitInline(text.slice(last, m.index), out);
    }
    let body = m[1];
    // Strip an optional language hint: a first line containing no whitespace,
    // followed by a newline. `"js\ncode"` -> `"code"`. `"code"` -> `"code"`.
    const nl = body.indexOf("\n");
    if (nl > 0 && !/\s/.test(body.slice(0, nl))) {
      body = body.slice(nl + 1);
    }
    // Strip a single leading newline when the fence opened on its own line.
    if (body.startsWith("\n")) body = body.slice(1);
    // Strip a single trailing newline before the closing fence.
    if (body.endsWith("\n")) body = body.slice(0, -1);
    out.push({ type: "codeblock", content: body });
    last = m.index + m[0].length;
  }
  if (last < text.length) {
    splitInline(text.slice(last), out);
  }
  return out;
}

function splitInline(text: string, out: InlineSegment[]): void {
  let last = 0;
  INLINE_CODE.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = INLINE_CODE.exec(text)) !== null) {
    if (m.index > last) {
      out.push({ type: "text", content: text.slice(last, m.index) });
    }
    out.push({ type: "code", content: m[1] });
    last = m.index + m[0].length;
  }
  if (last < text.length) {
    out.push({ type: "text", content: text.slice(last) });
  }
}

/**
 * Render user-typed text with Slack-style backtick formatting.
 * - ```fenced``` -> <pre><code>
 * - `inline`    -> <code>
 * - URLs in plain-text segments become clickable <a>.
 * - URLs inside code are NOT linkified.
 */
export function renderInlineText(text: string): React.ReactNode {
  const segments = parseInlineSegments(text);
  return segments.map((seg, i) => {
    if (seg.type === "codeblock") {
      return (
        <pre key={i} className="inline-code-block">
          <code>{seg.content}</code>
        </pre>
      );
    }
    if (seg.type === "code") {
      return (
        <code key={i} className="inline-code">
          {seg.content}
        </code>
      );
    }
    return <React.Fragment key={i}>{linkifyPlain(seg.content)}</React.Fragment>;
  });
}

function linkifyPlain(text: string): React.ReactNode {
  const parts = parseLinks(text);
  if (parts.length === 0) return text;
  if (parts.length === 1 && parts[0].type === "text") return text;
  return parts.map((p, i) => {
    if (p.type === "link") {
      return (
        <a key={i} href={p.href} target="_blank" rel="noopener noreferrer" className="text-link">
          {p.content}
        </a>
      );
    }
    return <React.Fragment key={i}>{p.content}</React.Fragment>;
  });
}
