// Highlighting for Go text/template system prompts: the {{actions}} stand out
// from the prose around them, and the action a TemplateProblem points at is
// marked.
import type { TemplateProblem } from "../../types";

const ACTION = /\{\{[\s\S]*?\}\}/g;
const TOKEN =
  /("(?:[^"\\\n]|\\.)*"|`[^`]*`)|(\$\w*(?:\.\w+)*|(?:\.\w+)+|\.)|\b(if|else|end|range|with|define|template|block|break|continue|and|or|not|len|index|eq|ne|lt|le|gt|ge|print|printf|println|slice|call)\b/g;

function escape(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

function action(text: string): string {
  const open = text.match(/^\{\{-?/)![0];
  const close = text.match(/-?\}\}$/)![0];
  const inner = text.slice(open.length, text.length - close.length);
  let body = "";
  if (/^\s*\/\*[\s\S]*\*\/\s*$/.test(inner)) {
    body = `<span class="tpl-comment">${escape(inner)}</span>`;
  } else {
    let last = 0;
    for (const m of inner.matchAll(TOKEN)) {
      const cls = m[1] ? "tpl-string" : m[2] ? "tpl-field" : "tpl-keyword";
      body += escape(inner.slice(last, m.index)) + `<span class="${cls}">${escape(m[0])}</span>`;
      last = m.index + m[0].length;
    }
    body += escape(inner.slice(last));
  }
  return `<span class="tpl-action"><span class="tpl-delim">${open}</span>${body}<span class="tpl-delim">${close}</span></span>`;
}

function highlight(text: string): string {
  let out = "";
  let last = 0;
  for (const m of text.matchAll(ACTION)) {
    out += escape(text.slice(last, m.index)) + action(m[0]);
    last = m.index + m[0].length;
  }
  return out + escape(text.slice(last));
}

/** Where in text a problem is: the action at its line and column, or else
 *  the rest of the line from the column (the whole line without one). */
export function problemRange(text: string, p: TemplateProblem): [number, number] {
  if (!p.line) return [0, 0];
  let lineStart = 0;
  for (let line = 1; line < p.line; line++) {
    const next = text.indexOf("\n", lineStart);
    if (next < 0) break;
    lineStart = next + 1;
  }
  const newline = text.indexOf("\n", lineStart);
  const lineEnd = newline < 0 ? text.length : newline;
  if (!p.column) return [lineStart, lineEnd];
  // The column counts characters, which a string may hold two units of.
  const before = Array.from(text.slice(lineStart, lineEnd)).slice(0, p.column - 1);
  const at = lineStart + before.join("").length;
  for (const m of text.matchAll(ACTION)) {
    if (m.index <= at && at < m.index + m[0].length) return [m.index, m.index + m[0].length];
  }
  return [at, lineEnd];
}

/** text as HTML, highlighted, with problem's part marked. */
export function highlightTemplate(text: string, problem: TemplateProblem | null): string {
  // A trailing newline needs something after it to take up its line.
  const shown = text.endsWith("\n") || text === "" ? text + " " : text;
  if (!problem) return highlight(shown);
  const [a, b] = problemRange(text, problem);
  if (a === b && !problem.line) return highlight(shown);
  return (
    highlight(shown.slice(0, a)) +
    `<mark class="tpl-error">${highlight(shown.slice(a, Math.max(b, a + 1)))}</mark>` +
    highlight(shown.slice(Math.max(b, a + 1)))
  );
}
