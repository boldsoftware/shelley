const TAG_PREFIX = "tag:";
const USER_PREFIX = "user:";
export const UNREAD_TERM = "is:unread";
export const UNTAGGED_TERM = "is:untagged";
export const UNATTRIBUTED_TERM = "is:unattributed";

export interface QueryTermSpan {
  raw: string;
  start: number;
  end: number;
}

export interface ScannedConversationQuery {
  terms: QueryTermSpan[];
  endsMidTerm: boolean;
}

export type StructuredQueryKind = "tag" | "user" | "untagged" | "unattributed" | "unread";
export type EditableStructuredQueryKind = Extract<StructuredQueryKind, "tag" | "user">;

export interface StructuredQueryEdit {
  kind: EditableStructuredQueryKind;
  start: number;
  end: number;
}

export interface ActiveStructuredQueryEdit extends StructuredQueryEdit {
  prefix: string | null;
}

export interface ConversationQuerySelection {
  anchor: number;
  focus: number;
}

interface ConversationQueryChange {
  start: number;
  previousEnd: number;
  nextEnd: number;
}

export interface QueryTextToken {
  kind: "text";
  raw: string;
  start: number;
  end: number;
}

export interface StructuredQueryToken {
  kind: StructuredQueryKind;
  raw: string;
  start: number;
  end: number;
  label: string;
  value: string;
}

export type ConversationQueryToken = QueryTextToken | StructuredQueryToken;

function assertQueryRange(raw: string, start: number, end: number): void {
  if (start < 0 || end < start || end > raw.length) {
    throw new Error(`Invalid conversation query range ${start}:${end}`);
  }
}

// Finds whitespace-delimited query terms while treating a quoted span as one
// term. Ranges refer to the original query so editor operations never have to
// rebuild unrelated text or quoted tag values.
export function scanConversationQuery(raw: string): ScannedConversationQuery {
  const terms: QueryTermSpan[] = [];
  let start = -1;
  let inQuote = false;
  let escaped = false;

  for (let i = 0; i < raw.length; i += 1) {
    const ch = raw[i];
    if (start < 0) {
      if (/\s/.test(ch)) continue;
      start = i;
    }
    if (escaped) {
      escaped = false;
      continue;
    }
    if (inQuote && ch === "\\") {
      escaped = true;
      continue;
    }
    if (ch === '"') {
      inQuote = !inQuote;
      continue;
    }
    if (!inQuote && /\s/.test(ch)) {
      terms.push({ raw: raw.slice(start, i), start, end: i });
      start = -1;
    }
  }

  if (start >= 0) terms.push({ raw: raw.slice(start), start, end: raw.length });
  return { terms, endsMidTerm: start >= 0 };
}

// Decodes the value portion of a tag term. An unterminated quoted value is
// valid while it is still being typed.
export function decodeTagQueryValue(value: string): string {
  if (!value.startsWith('"')) return value;
  let out = "";
  let escaped = false;
  for (const ch of value.slice(1)) {
    if (escaped) {
      out += ch;
      escaped = false;
      continue;
    }
    if (ch === "\\") {
      escaped = true;
      continue;
    }
    if (ch === '"') break;
    out += ch;
  }
  return out;
}

function structuredToken(
  term: QueryTermSpan,
  trailingPartial: boolean,
): StructuredQueryToken | null {
  const lower = term.raw.toLowerCase();
  if (lower === UNREAD_TERM) {
    return { ...term, kind: "unread", label: UNREAD_TERM, value: "" };
  }
  if (lower === UNTAGGED_TERM) {
    return { ...term, kind: "untagged", label: UNTAGGED_TERM, value: "" };
  }
  if (lower === UNATTRIBUTED_TERM) {
    return { ...term, kind: "unattributed", label: UNATTRIBUTED_TERM, value: "" };
  }
  if (lower.startsWith(TAG_PREFIX)) {
    if (trailingPartial) return null;
    const value = decodeTagQueryValue(term.raw.slice(TAG_PREFIX.length)).trim();
    if (!value) return null;
    return { ...term, kind: "tag", label: `${TAG_PREFIX}${value}`, value };
  }
  if (lower.startsWith(USER_PREFIX)) {
    if (trailingPartial) return null;
    const value = term.raw.slice(USER_PREFIX.length).trim();
    if (!value) return null;
    return { ...term, kind: "user", label: `${USER_PREFIX}${value}`, value };
  }
  return null;
}

// Produces alternating editable text and atomic structured terms. Empty text
// tokens at the edges are deliberate insertion points before/after a pill.
export function tokenizeConversationQuery(raw: string): ConversationQueryToken[] {
  const scanned = scanConversationQuery(raw);
  const structured: StructuredQueryToken[] = [];
  scanned.terms.forEach((term, index) => {
    const token = structuredToken(term, scanned.endsMidTerm && index === scanned.terms.length - 1);
    if (token) structured.push(token);
  });

  const tokens: ConversationQueryToken[] = [];
  let textStart = 0;
  for (const token of structured) {
    tokens.push({
      kind: "text",
      raw: raw.slice(textStart, token.start),
      start: textStart,
      end: token.start,
    });
    tokens.push(token);
    textStart = token.end;
  }
  tokens.push({
    kind: "text",
    raw: raw.slice(textStart),
    start: textStart,
    end: raw.length,
  });
  return tokens;
}

export function activeStructuredQueryEdit(
  raw: string,
  edit: StructuredQueryEdit,
): ActiveStructuredQueryEdit {
  assertQueryRange(raw, edit.start, edit.end);
  const term = raw.slice(edit.start, edit.end);
  const expectedPrefix = edit.kind === "tag" ? TAG_PREFIX : USER_PREFIX;
  const prefix = term.toLowerCase().startsWith(expectedPrefix)
    ? edit.kind === "tag"
      ? decodeTagQueryValue(term.slice(expectedPrefix.length))
      : term.slice(expectedPrefix.length)
    : null;
  return { ...edit, prefix };
}

// Returns the editable tag/user term at one raw caret offset. Offsets on the
// end edge belong to the preceding term so autocomplete keeps following the
// value immediately after a character is inserted.
export function structuredQueryEditAtOffset(
  raw: string,
  offset: number,
): ActiveStructuredQueryEdit | null {
  assertQueryRange(raw, offset, offset);
  const term = scanConversationQuery(raw).terms.find(
    (candidate) => offset >= candidate.start && offset <= candidate.end,
  );
  if (!term) return null;
  const lower = term.raw.toLowerCase();
  const kind = lower.startsWith(TAG_PREFIX) ? "tag" : lower.startsWith(USER_PREFIX) ? "user" : null;
  if (!kind) return null;
  return activeStructuredQueryEdit(raw, { kind, start: term.start, end: term.end });
}

export function omitStructuredQueryEdit(raw: string, edit: StructuredQueryEdit): string {
  assertQueryRange(raw, edit.start, edit.end);
  return raw.slice(0, edit.start) + raw.slice(edit.end);
}

// Completion of a trailing partial also adds its committing separator. A
// middle edit already has its surrounding query text and replaces only its
// exact lexical range.
export function completeStructuredQueryEdit(
  raw: string,
  edit: StructuredQueryEdit,
  term: string,
): { query: string; caret: number } {
  assertQueryRange(raw, edit.start, edit.end);
  const suffix = raw.slice(edit.end).replace(/^\s*/, "");
  const replacement = `${term} `;
  return {
    query: raw.slice(0, edit.start) + replacement + suffix,
    caret: edit.start + replacement.length,
  };
}

function conversationQueryChange(previous: string, next: string): ConversationQueryChange | null {
  if (previous === next) return null;
  let start = 0;
  while (start < previous.length && start < next.length && previous[start] === next[start]) {
    start += 1;
  }

  let previousEnd = previous.length;
  let nextEnd = next.length;
  while (
    previousEnd > start &&
    nextEnd > start &&
    previous[previousEnd - 1] === next[nextEnd - 1]
  ) {
    previousEnd -= 1;
    nextEnd -= 1;
  }
  return { start, previousEnd, nextEnd };
}

function rebaseQueryOffset(
  offset: number,
  change: ConversationQueryChange,
  affinity: "before" | "after",
): number {
  if (offset < change.start || (offset === change.start && affinity === "before")) return offset;
  if (offset > change.previousEnd || (offset === change.previousEnd && affinity === "after")) {
    return offset + change.nextEnd - change.previousEnd;
  }
  return affinity === "before" ? change.start : change.nextEnd;
}

export function rebaseConversationQuerySelection(
  previous: string,
  next: string,
  selection: ConversationQuerySelection,
): ConversationQuerySelection {
  assertQueryRange(previous, selection.anchor, selection.anchor);
  assertQueryRange(previous, selection.focus, selection.focus);
  const change = conversationQueryChange(previous, next);
  if (!change) return { ...selection };
  if (selection.anchor === selection.focus) {
    const offset = rebaseQueryOffset(selection.focus, change, "after");
    return { anchor: offset, focus: offset };
  }
  const forward = selection.anchor < selection.focus;
  return {
    anchor: rebaseQueryOffset(selection.anchor, change, forward ? "before" : "after"),
    focus: rebaseQueryOffset(selection.focus, change, forward ? "after" : "before"),
  };
}

export interface RemovedConversationQueryToken {
  query: string;
  caret: number;
}

// Removes one exact term and one adjoining separator. Prefer the following
// separator so text before and after the removed term keep a single natural
// boundary.
export function removeConversationQueryTerm(
  raw: string,
  termStart: number,
  termEnd: number,
): RemovedConversationQueryToken {
  assertQueryRange(raw, termStart, termEnd);
  let start = termStart;
  let end = termEnd;
  if (end < raw.length && /\s/.test(raw[end])) {
    while (end < raw.length && /\s/.test(raw[end])) end += 1;
  } else {
    while (start > 0 && /\s/.test(raw[start - 1])) start -= 1;
  }
  return { query: raw.slice(0, start) + raw.slice(end), caret: start };
}

// Escape clears editable text/partials while retaining committed pills in
// their lexical order.
export function clearConversationQueryText(
  raw: string,
  editableTerm?: StructuredQueryEdit | null,
): string {
  const structured = tokenizeConversationQuery(raw).filter(
    (token) =>
      token.kind !== "text" &&
      (!editableTerm || token.end <= editableTerm.start || token.start >= editableTerm.end),
  );
  return structured.length === 0 ? "" : structured.map((token) => token.raw).join(" ") + " ";
}
