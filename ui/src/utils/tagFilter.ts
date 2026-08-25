// Pure helpers backing the conversation drawer's tag handling: the `tag:`
// search-query syntax, the AND filter it drives, and tag-based grouping.
// Comparison is case-folded (the server's normalizeTags is case-sensitive,
// so `Infra` and `infra` can both exist); display keeps first-seen casing.
// The selection lives only in the search query the user typed — nothing can
// be silently filtering. No Vue imports: plain functions over conversation
// rows.
import type { Conversation } from "../types";
import { parseTags } from "../vue/components/conversationDrawerShared";

// The comparison key for a tag. Empty string means "not a tag".
export function foldTag(tag: string): string {
  return tag.trim().toLowerCase();
}

// The folded tags of one conversation, deduped (the server's normalizeTags is
// case-sensitive, so storage may hold both `Infra` and `infra`).
function foldedTagSet(conversation: Conversation): Set<string> {
  const out = new Set<string>();
  for (const tag of parseTags(conversation)) {
    const folded = foldTag(tag);
    if (folded) out.add(folded);
  }
  return out;
}

function foldedSelection(selected: readonly string[]): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const tag of selected) {
    const folded = foldTag(tag);
    if (!folded || seen.has(folded)) continue;
    seen.add(folded);
    out.push(folded);
  }
  return out;
}

// Keeps every conversation carrying all of `selected`. An empty selection
// returns the input array unchanged (same reference) so the no-filter case
// costs nothing and does not churn downstream computeds.
export function filterConversationsByTags<T extends Conversation>(
  conversations: T[],
  selected: readonly string[],
): T[] {
  const wanted = foldedSelection(selected);
  if (wanted.length === 0) return conversations;
  return conversations.filter((conversation) => {
    const has = foldedTagSet(conversation);
    return wanted.every((tag) => has.has(tag));
  });
}

// The tag half of a parsed query: the AND over `tag:` terms, plus
// `is:untagged`. One function so the drawer's three lists cannot drift.
export function filterConversationsByQuery<T extends Conversation>(
  conversations: T[],
  query: TagQuery,
): T[] {
  const byTag = filterConversationsByTags(conversations, query.tags);
  if (!query.untaggedOnly) return byTag;
  return byTag.filter((conversation) => foldedTagSet(conversation).size === 0);
}

// Whether the query narrows by tag at all. Drives "is the tag filter to blame
// for this empty list".
export function queryHasTagFilter(query: TagQuery): boolean {
  return query.tags.length > 0 || query.untaggedOnly;
}

// Removes `is:untagged` from a query, leaving everything else alone.
export function removeUntaggedFromQuery(raw: string): string {
  const kept = tokenize(raw).terms.filter((term) => term.toLowerCase() !== UNTAGGED_TERM);
  return kept.length === 0 ? "" : kept.join(" ") + " ";
}

export type TagQuery = Pick<ParsedQuery, "tags" | "untaggedOnly">;

export interface OfferedTag {
  // First-seen original casing, for display.
  tag: string;
  // How many conversations would remain if this tag were added to the
  // selection.
  count: number;
}

// The tags that can still be added, each with the size of the result set it
// would produce. Sorted by count descending, then alphabetically.
export function offeredTags(
  conversations: Conversation[],
  selected: readonly string[],
): OfferedTag[] {
  const chosen = new Set(foldedSelection(selected));
  const counts = new Map<string, OfferedTag>();
  for (const conversation of filterConversationsByTags(conversations, selected)) {
    const seen = new Set<string>();
    for (const tag of parseTags(conversation)) {
      const folded = foldTag(tag);
      if (!folded || chosen.has(folded) || seen.has(folded)) continue;
      seen.add(folded);
      const existing = counts.get(folded);
      if (existing) existing.count += 1;
      else counts.set(folded, { tag: tag.trim(), count: 1 });
    }
  }
  return [...counts.values()].sort(
    (a, b) => b.count - a.count || foldTag(a.tag).localeCompare(foldTag(b.tag)),
  );
}

// The row tag editor's dropdown: existing tags matching `typed` anywhere in
// their text (not just as a prefix), each with its usage count, ranked so the
// best pick is first. `exclude` holds the tags already on the conversation
// being edited, which cannot usefully be added again. An empty `typed` offers
// the whole vocabulary (most-used first). Prefix matches sort ahead of
// mid-string matches; offeredTags' count-then-alpha order breaks ties (the
// sort is stable). The first entry is the "best match" Enter commits.
export function matchTags(
  conversations: Conversation[],
  typed: string,
  exclude: readonly string[],
): OfferedTag[] {
  const needle = foldTag(typed);
  const excluded = new Set(exclude.map(foldTag));
  const all = offeredTags(conversations, []).filter((o) => !excluded.has(foldTag(o.tag)));
  if (!needle) return all;
  const matched = all.filter((o) => foldTag(o.tag).includes(needle));
  return matched.sort((a, b) => {
    const ap = foldTag(a.tag).startsWith(needle) ? 0 : 1;
    const bp = foldTag(b.tag).startsWith(needle) ? 0 : 1;
    return ap - bp;
  });
}

export function isTagSelected(selected: readonly string[], tag: string): boolean {
  const folded = foldTag(tag);
  return folded !== "" && selected.some((t) => foldTag(t) === folded);
}

// Substring match used by the picker's search box.
export function tagMatchesQuery(tag: string, query: string): boolean {
  const needle = foldTag(query);
  if (!needle) return true;
  return foldTag(tag).includes(needle);
}

// --- The `tag:` search-query syntax ---
//
// Tags are typed into the ordinary search box: `tag:infra auth bug` means
// "carries #infra, and matches the text 'auth bug'". One input narrows the
// list, and the active filter is visible and editable as text.
//
// "Show me the untagged ones" is `is:untagged`, in a separate namespace:
// `tag:untagged` would collide with a tag actually named "untagged".

const TAG_PREFIX = "tag:";
export const UNTAGGED_TERM = "is:untagged";

export interface ParsedQuery {
  // Committed tags (followed by a space or another term), in the order typed.
  tags: string[];
  // Everything that was not a `tag:` term, for the full-text search.
  text: string;
  // True when `is:untagged` is present: keep only conversations with no tags.
  untaggedOnly: boolean;
  // The half-typed trailing `tag:` term — a bare `tag:`, or a partial
  // `tag:inf`. Deliberately NOT part of `tags`: a half-typed tag matches no
  // conversation, so filtering on it would empty the list and starve the
  // dropdown computed from what survives. It queries the vocabulary instead.
  activeTagPrefix: string | null;
}

// Splits a raw query into whitespace-separated terms, except that a double
// quote opens a span in which spaces are literal — so `tag:"in progress"` is
// one term. Inside a quoted span, backslash escapes the next character, so a
// tag containing a literal quote is `tag:"a \" mark"`. An unterminated quote
// runs to the end of the string, which is the still-being-typed case, and
// also means the caret is mid-term regardless of a trailing space.
function tokenize(raw: string): { terms: string[]; endsMidTerm: boolean } {
  const terms: string[] = [];
  let current = "";
  let inQuote = false;
  let escaped = false;
  for (const ch of raw) {
    if (escaped) {
      current += ch;
      escaped = false;
      continue;
    }
    if (inQuote && ch === "\\") {
      escaped = true;
      current += ch;
      continue;
    }
    if (ch === '"') {
      inQuote = !inQuote;
      current += ch;
      continue;
    }
    if (!inQuote && /\s/.test(ch)) {
      if (current !== "") {
        terms.push(current);
        current = "";
      }
      continue;
    }
    current += ch;
  }
  if (current !== "") terms.push(current);
  return { terms, endsMidTerm: current !== "" };
}

// Strips the quotes and backslash escapes from a `tag:` term's value.
// Tolerates an unterminated quote (`"in prog`) so mid-typing still yields a
// usable prefix, including a trailing lone backslash (an escape being typed).
function unquoteTagValue(value: string): string {
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

// The token for one tag, quoted only when it has to be: tags can contain
// spaces and quote marks (the server allows them), and an unquoted
// `tag:in progress` would split into the tag `in` plus the word `progress`.
// Inside the quotes, `\` and `"` are backslash-escaped.
export function formatTagTerm(tag: string): string {
  const trimmed = tag.trim();
  if (!/[\s"]/.test(trimmed)) return `${TAG_PREFIX}${trimmed}`;
  return `${TAG_PREFIX}"${trimmed.replace(/[\\"]/g, "\\$&")}"`;
}

// Splits a raw search query into tag terms and free text.
export function parseSearchQuery(raw: string): ParsedQuery {
  const tags: string[] = [];
  const seen = new Set<string>();
  const words: string[] = [];
  const { terms, endsMidTerm } = tokenize(raw);
  let untaggedOnly = false;
  terms.forEach((term, i) => {
    if (term.toLowerCase() === UNTAGGED_TERM) {
      untaggedOnly = true;
      return;
    }
    if (!term.toLowerCase().startsWith(TAG_PREFIX)) {
      words.push(term);
      return;
    }
    const value = unquoteTagValue(term.slice(TAG_PREFIX.length));
    const folded = foldTag(value);
    // The final term keeps its own line below; everything else is committed.
    if (i === terms.length - 1 && endsMidTerm) return;
    if (!folded || seen.has(folded)) return;
    seen.add(folded);
    tags.push(value.trim());
  });

  const last = terms[terms.length - 1];
  let activeTagPrefix: string | null = null;
  if (endsMidTerm && last && last.toLowerCase().startsWith(TAG_PREFIX)) {
    activeTagPrefix = unquoteTagValue(last.slice(TAG_PREFIX.length));
  }

  return { tags, text: words.join(" "), untaggedOnly, activeTagPrefix };
}

// Replaces the `tag:` term the caret sits in with `term`, followed by a space
// so the next keystroke starts a fresh term. Used when a dropdown entry is
// chosen — `term` is a full token (`tag:infra`, `tag:"in progress"`, or
// `is:untagged`).
export function completeTermInQuery(raw: string, term: string): string {
  const { terms, endsMidTerm } = tokenize(raw);
  const last = terms[terms.length - 1];
  const replacing = last !== undefined && last.toLowerCase().startsWith(TAG_PREFIX) && endsMidTerm;
  const kept = replacing ? terms.slice(0, -1) : terms;
  return [...kept, term].join(" ") + " ";
}

// Removes every `tag:` term matching `tag`, leaving the rest of the query
// alone. Used by the chips and by clicking an already-selected tag.
export function removeTagFromQuery(raw: string, tag: string): string {
  const target = foldTag(tag);
  const kept = tokenize(raw).terms.filter((term) => {
    if (!term.toLowerCase().startsWith(TAG_PREFIX)) return true;
    return foldTag(unquoteTagValue(term.slice(TAG_PREFIX.length))) !== target;
  });
  return kept.length === 0 ? "" : kept.join(" ") + " ";
}

// Adds `tag:<tag>` to a query, or removes it if already present. Used by the
// row chips, which toggle.
export function toggleTagInQuery(raw: string, tag: string): string {
  if (isTagSelected(parseSearchQuery(raw).tags, tag)) return removeTagFromQuery(raw, tag);
  const base = raw.replace(/\s+$/, "");
  return (base ? base + " " : "") + formatTagTerm(tag) + " ";
}

// --- Grouping ---

// The group key for "group by tag": the conversation's whole tag set, sorted
// and deduped, so `#infra #urgent` and `#urgent #infra` land in one group and
// every conversation appears exactly once (a tag-per-group layout would
// duplicate rows). NUL-joined so the parts stay recoverable: no tag can
// contain one.
const TAG_KEY_SEP = "\u0000";

export function tagGroupKey(conversation: Conversation): string | null {
  const folded = [...foldedTagSet(conversation)].sort();
  return folded.length === 0 ? null : folded.join(TAG_KEY_SEP);
}

// Orders tag groups by first tag, then second, and so on — so `#a #b`
// precedes `#b`, which precedes `#b #c`. Compares tag tuples rather than the
// joined keys: collation may ignore the NUL separator.
export function compareTagGroupKeys(a: string, b: string): number {
  const left = a.split(TAG_KEY_SEP);
  const right = b.split(TAG_KEY_SEP);
  for (let i = 0; i < Math.min(left.length, right.length); i++) {
    const cmp = left[i].localeCompare(right[i]);
    if (cmp !== 0) return cmp;
  }
  return left.length - right.length;
}

// The display label for a tagGroupKey: original casing, sorted, `#`-prefixed.
export function tagGroupLabel(conversation: Conversation): string {
  const byFold = new Map<string, string>();
  for (const tag of parseTags(conversation)) {
    const folded = foldTag(tag);
    if (folded && !byFold.has(folded)) byFold.set(folded, tag.trim());
  }
  return [...byFold.keys()]
    .sort()
    .map((folded) => `#${byFold.get(folded)}`)
    .join(" ");
}
