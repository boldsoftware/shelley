import type { Conversation } from "../types";
import {
  filterConversationsByTags,
  foldTag,
  compareTagGroupKeys,
  completeTermInQuery,
  filterConversationsByQuery,
  formatTagTerm,
  formatUserTerm,
  matchTags,
  offeredTags,
  parseSearchQuery,
  rankExactMatchFirst,
  removeTagFromQuery,
  removeUnattributedFromQuery,
  removeUntaggedFromQuery,
  removeUserFromQuery,
  startFilterTermInQuery,
  tagGroupKey,
  tagGroupLabel,
  tagMatchesQuery,
  toggleTagInQuery,
  toggleUnreadInQuery,
} from "./tagFilter";

function assert(cond: boolean, msg: string): void {
  if (!cond) throw new Error(`Assertion failed: ${msg}`);
}

function assertEqual(got: string[], want: string[], msg: string): void {
  const g = JSON.stringify(got);
  const w = JSON.stringify(want);
  if (g !== w) throw new Error(`Assertion failed: ${msg} (got ${g}, want ${w})`);
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

function conv(id: string, tags: string[] | string | null): Conversation {
  return {
    conversation_id: id,
    slug: id,
    user_initiated: true,
    created_at: "2026-05-10T12:00:00Z",
    updated_at: "2026-05-10T12:00:00Z",
    cwd: null,
    archived: false,
    parent_conversation_id: null,
    tags: tags === null ? "" : typeof tags === "string" ? tags : JSON.stringify(tags),
  } as unknown as Conversation;
}

function ids(list: Conversation[]): string {
  return list.map((c) => c.conversation_id).join(",");
}

function offered(list: Conversation[], selected: string[]): string {
  return offeredTags(list, selected)
    .map((o) => `${o.tag}:${o.count}`)
    .join(" ");
}

// A small corpus with deliberate cross-cutting overlaps.
//   a: infra, urgent      b: infra            c: urgent, docs
//   d: docs               e: (untagged)
const corpus = [
  conv("a", ["infra", "urgent"]),
  conv("b", ["infra"]),
  conv("c", ["urgent", "docs"]),
  conv("d", ["docs"]),
  conv("e", null),
];

run("empty selection returns everything unchanged", () => {
  const out = filterConversationsByTags(corpus, []);
  assert(out === corpus, "same reference when nothing is selected");
  assert(ids(out) === "a,b,c,d,e", ids(out));
});

run("AND semantics across one, two and three tags", () => {
  assert(ids(filterConversationsByTags(corpus, ["infra"])) === "a,b", "one tag");
  assert(ids(filterConversationsByTags(corpus, ["infra", "urgent"])) === "a", "two tags");
  const triple = [conv("x", ["a", "b", "c"]), conv("y", ["a", "b"])];
  assert(ids(filterConversationsByTags(triple, ["a", "b", "c"])) === "x", "three tags");
  assert(ids(filterConversationsByTags(triple, ["a", "b"])) === "x,y", "prefix of three");
});

run("untagged conversations drop out as soon as any tag is selected", () => {
  assert(!ids(filterConversationsByTags(corpus, ["docs"])).includes("e"), "untagged excluded");
  assert(ids(filterConversationsByTags(corpus, [])).includes("e"), "untagged kept when unfiltered");
});

run("tag filters never remove subagents", () => {
  const subagent = conv("subagent", ["other"]);
  subagent.parent_conversation_id = "a";
  assert(
    ids(filterConversationsByTags([...corpus, subagent], ["infra"])) === "a,b,subagent",
    "selected tags retain subagents",
  );
  assert(
    ids(
      filterConversationsByQuery([...corpus, subagent], {
        tags: [],
        untaggedOnly: true,
      }),
    ) === "e,subagent",
    "untagged retains subagents",
  );
});

run("a non-co-occurring combination yields nothing", () => {
  // infra and docs never appear together.
  assert(ids(filterConversationsByTags(corpus, ["infra", "docs"])) === "", "disjoint pair");
});

run("offered tags carry the count of the result set they would produce", () => {
  assert(offered(corpus, []) === "docs:2 infra:2 urgent:2", offered(corpus, []));
  assert(offered(corpus, ["infra"]) === "urgent:1", offered(corpus, ["infra"]));
  assert(offered(corpus, ["urgent"]) === "docs:1 infra:1", offered(corpus, ["urgent"]));
});

run("offered tags never include a tag that does not co-occur", () => {
  // With infra selected only `a` and `b` remain; docs is on neither, so it is
  // never offered and a zero-result state is unreachable by selecting.
  const list = offeredTags(corpus, ["infra"]);
  assert(!list.some((o) => o.tag === "docs"), "docs not offered alongside infra");
  assert(
    list.every((o) => o.count > 0),
    "every offered tag keeps at least one conversation",
  );
});

run("offered tags exclude what is already selected", () => {
  assert(!offeredTags(corpus, ["infra"]).some((o) => o.tag === "infra"), "selected tag dropped");
});

run("matchTags matches anywhere and ranks prefix hits first", () => {
  const pool = [
    conv("1", ["terminal-work", "integration"]),
    conv("2", ["terminal-work", "integration"]),
    conv("3", ["in-progress"]),
  ];
  const names = (typed: string, exclude: string[] = []) =>
    matchTags(pool, typed, exclude).map((o) => o.tag);
  // Empty query offers the whole vocabulary, most-used first.
  assertEqual(names(""), ["integration", "terminal-work", "in-progress"], "empty offers all");
  // `work` appears mid-string in terminal-work, and as a prefix in workflow;
  // the prefix hit ranks first even though it is less used.
  const pool2 = [conv("1", ["terminal-work", "workflow"]), conv("2", ["terminal-work"])];
  assertEqual(
    matchTags(pool2, "work", []).map((o) => o.tag),
    ["workflow", "terminal-work"],
    "prefix ranks first over more-used mid-string",
  );
  // Case-insensitive substring.
  assertEqual(names("WORK"), ["terminal-work"], "case-insensitive substring");
  assertEqual(names("zzz"), [], "no match");
  // Within one rank, more-used tags come first (offeredTags order is stable).
  const pool3 = [conv("1", ["terse", "terminal-work"]), conv("2", ["terminal-work"])];
  assertEqual(
    matchTags(pool3, "ter", []).map((o) => o.tag),
    ["terminal-work", "terse"],
    "most-used wins within a rank",
  );
  // Tags already on the conversation are excluded from its own suggestions.
  assertEqual(names("in", ["in-progress"]), ["integration", "terminal-work"], "own tags excluded");
  assertEqual(
    names("in", ["In-Progress"]),
    ["integration", "terminal-work"],
    "exclusion folds case",
  );
});

run("offered tags sort by count descending then alphabetically", () => {
  const list = [
    conv("1", ["zebra", "alpha"]),
    conv("2", ["zebra", "alpha"]),
    conv("3", ["zebra"]),
    conv("4", ["beta"]),
  ];
  assert(offered(list, []) === "zebra:3 alpha:2 beta:1", offered(list, []));
  const tie = [conv("1", ["beta", "alpha"]), conv("2", ["beta", "alpha"])];
  assert(offered(tie, []) === "alpha:2 beta:2", offered(tie, []));
});

run("matching is case-insensitive and display keeps the first-seen casing", () => {
  // The server's normalizeTags is case-sensitive, so both spellings can exist.
  const list = [conv("a", ["Infra"]), conv("b", ["infra"]), conv("c", ["INFRA", "Docs"])];
  assert(ids(filterConversationsByTags(list, ["infra"])) === "a,b,c", "lowercase query");
  assert(ids(filterConversationsByTags(list, ["INFRA"])) === "a,b,c", "uppercase query");
  assert(offered(list, []) === "Infra:3 Docs:1", offered(list, []));
  // A row holding both casings counts once.
  assert(offered([conv("z", ["Infra", "infra"])], []) === "Infra:1", "deduped within a row");
});

run("tag order in storage is irrelevant", () => {
  const forward = [conv("a", ["infra", "urgent"])];
  const reverse = [conv("a", ["urgent", "infra"])];
  assert(
    ids(filterConversationsByTags(forward, ["urgent", "infra"])) ===
      ids(filterConversationsByTags(reverse, ["infra", "urgent"])),
    "order-insensitive",
  );
});

run("malformed and empty tag payloads are tolerated", () => {
  const list = [conv("a", "not json"), conv("b", "[1,2,3]"), conv("c", null), conv("d", ["ok"])];
  assert(offered(list, []) === "ok:1", offered(list, []));
  assert(ids(filterConversationsByTags(list, ["ok"])) === "d", "only the valid row matches");
});

run("whitespace-only tags are ignored", () => {
  const list = [conv("a", ["  ", "real"])];
  assert(offered(list, []) === "real:1", offered(list, []));
  assert(ids(filterConversationsByTags(list, ["   "])) === "a", "blank selection is a no-op");
});

run("tagMatchesQuery does folded substring matching", () => {
  assert(tagMatchesQuery("Infrastructure", "fra"), "substring");
  assert(tagMatchesQuery("Infra", "INF"), "case-insensitive");
  assert(tagMatchesQuery("anything", "  "), "blank query matches all");
  assert(!tagMatchesQuery("docs", "infra"), "no match");
});

run("rankExactMatchFirst puts the typed value ahead of more popular superstrings", () => {
  // `alpha` is used once, `alpha-more` three times: count order would commit
  // the wrong tag on Space/Enter after typing exactly `alpha`.
  const pool = [
    conv("1", ["alpha-more"]),
    conv("2", ["alpha-more"]),
    conv("3", ["alpha-more", "alpha"]),
  ];
  const byCount = offeredTags(pool, []);
  assertEqual(
    byCount.map((o) => o.tag),
    ["alpha-more", "alpha"],
    "count order alone ranks the superstring first",
  );
  const rank = (typed: string) =>
    rankExactMatchFirst(byCount, (o) => o.tag, typed).map((o) => o.tag);
  assertEqual(rank("alpha"), ["alpha", "alpha-more"], "exact typed value first");
  assertEqual(rank(" ALPHA "), ["alpha", "alpha-more"], "exactness folds case and whitespace");
  assertEqual(rank("alph"), ["alpha-more", "alpha"], "a mere prefix keeps count order");
  assertEqual(rank(""), ["alpha-more", "alpha"], "nothing typed keeps count order");
  assert(rankExactMatchFirst(byCount, (o) => o.tag, "alph") === byCount, "no-op keeps reference");
  const users = [{ email: "me@x.com.au" }, { email: "Me@x.com" }];
  assertEqual(
    rankExactMatchFirst(users, (u) => u.email, "me@x.com").map((u) => u.email),
    ["Me@x.com", "me@x.com.au"],
    "emails rank the same way",
  );
});

run("foldTag trims and lowercases", () => {
  assert(foldTag("  Infra ") === "infra", "fold");
  assert(foldTag("   ") === "", "blank folds to empty");
});

run("parseSearchQuery splits tag terms from free text", () => {
  const q = parseSearchQuery("tag:infra auth bug");
  assertEqual(q.tags, ["infra"], "tags");
  assert(q.text === "auth bug", "text keeps word order");
  assert(q.activeTagPrefix === null, "a tag followed by more words is committed");
});

run("free text alone parses as no tags", () => {
  const q = parseSearchQuery("just searching");
  assertEqual(q.tags, [], "no tags");
  assertEqual(q.users, [], "no users");
  assert(q.text === "just searching", "text");
});

run("participant terms parse as a case-folded AND facet", () => {
  const q = parseSearchQuery(
    "user:Me@example.com user:other@example.com USER:me@example.com is:unattributed ",
  );
  assertEqual(q.users, ["Me@example.com", "other@example.com"], "users dedupe by folded email");
  assert(q.includeUnattributed, "unattributed included");
  assert(q.text === "", "structured terms do not leak into FTS");
});

run("a trailing user term stays visible and drives autocomplete until committed", () => {
  const partial = parseSearchQuery("deploy user:me@example.com");
  assertEqual(partial.users, [], "trailing user is not committed");
  assert(partial.text === "deploy", "partial stays out of FTS text");
  assert(partial.activeUserPrefix === "me@example.com", "partial drives user autocomplete");

  const committed = parseSearchQuery("deploy user:me@example.com ");
  assertEqual(committed.users, ["me@example.com"], "space commits the user");
  assert(committed.text === "deploy", "committed user leaves FTS text");
  assert(committed.activeUserPrefix === null, "committed user closes autocomplete");
});

run("formatUserTerm emits the literal query syntax", () => {
  assert(formatUserTerm(" me@example.com ") === "user:me@example.com", "trimmed");
  assert(
    completeTermInQuery("tag:infra user:me", "user:me@example.com") ===
      "tag:infra user:me@example.com ",
    "user completion replaces the active term",
  );
});

run("filter actions replace a trailing structured prefix", () => {
  assert(
    startFilterTermInQuery("user:me@example.com user:", "tag:") === "user:me@example.com tag:",
    "tag replaces user",
  );
  assert(
    startFilterTermInQuery("user:me@example.com tag:", "user:") === "user:me@example.com user:",
    "user replaces tag",
  );
  assert(
    startFilterTermInQuery("user:me@example.com user:", "user:") === "user:me@example.com user:",
    "repeated click is idempotent",
  );
  assert(
    startFilterTermInQuery('find  tag:"in progress"  user:par', "tag:") ===
      'find  tag:"in progress"  tag:',
    "replacement preserves preceding text, spacing, and quotes",
  );
});

run("a bare trailing tag: opens the dropdown and filters nothing", () => {
  const q = parseSearchQuery("tag:");
  assertEqual(q.tags, [], "an empty tag is not a filter");
  assert(q.activeTagPrefix === "", "bare prefix is active");
  assert(q.text === "", "no free text");
});

run("a partial tag queries the vocabulary without filtering", () => {
  const q = parseSearchQuery("tag:inf");
  // Filtering on a half-typed tag would empty the list, and with it the
  // dropdown that is supposed to help finish the word.
  assertEqual(q.tags, [], "half-typed tags do not filter");
  assert(q.activeTagPrefix === "inf", "but do drive the dropdown");
});

run("a trailing space commits the tag and closes the dropdown", () => {
  const q = parseSearchQuery("tag:infra ");
  assertEqual(q.tags, ["infra"], "tag committed");
  assert(q.activeTagPrefix === null, "no longer active");
});

run("multiple tag terms AND together", () => {
  const q = parseSearchQuery("tag:infra tag:urgent ");
  assertEqual(q.tags, ["infra", "urgent"], "both tags");
  assert(q.text === "", "no free text");
});

run("tag: is recognised case-insensitively and duplicates collapse", () => {
  const q = parseSearchQuery("TAG:Infra tag:infra ");
  assertEqual(q.tags, ["Infra"], "first-seen casing wins, duplicate dropped");
});

run("text before a tag term is kept", () => {
  const q = parseSearchQuery("deploy tag:infra ");
  assert(q.text === "deploy", "leading text survives");
  assertEqual(q.tags, ["infra"], "trailing tag parsed");
});

run("a quoted tag value keeps its spaces", () => {
  // The server allows tags with spaces, so the syntax must express them:
  // unquoted, `tag:in progress` would be the tag `in` plus the word
  // `progress`.
  const q = parseSearchQuery('tag:"in progress" auth ');
  assertEqual(q.tags, ["in progress"], "quoted tag is one term");
  assert(q.text === "auth", "free text unaffected");
});

run("an unterminated quote is still mid-typing", () => {
  // The closing quote is not typed yet — even the space must not commit,
  // because inside quotes a space is part of the value.
  const q = parseSearchQuery('tag:"in prog');
  assertEqual(q.tags, [], "not committed");
  assert(q.activeTagPrefix === "in prog", "prefix spans the space");
  const mid = parseSearchQuery('tag:"in ');
  assertEqual(mid.tags, [], "space inside quotes does not commit");
  assert(mid.activeTagPrefix === "in ", "prefix keeps the space");
});

run("formatTagTerm quotes only when it must", () => {
  assert(formatTagTerm("infra") === "tag:infra", "no quotes without spaces");
  assert(formatTagTerm("in progress") === 'tag:"in progress"', "quoted with spaces");
  assert(formatTagTerm('a"b') === 'tag:"a\\"b"', "a quote forces quoting, escaped");
});

run("a tag containing quote marks round-trips through its escaped form", () => {
  // The server allows quote marks in tags; the term escapes them with a
  // backslash so the quoted span does not end early.
  const tag = 'a tag" with a quote';
  const term = formatTagTerm(tag);
  assert(term === 'tag:"a tag\\" with a quote"', term);
  const q = parseSearchQuery(term + " ");
  assertEqual(q.tags, [tag], "parses back to the same tag");
  assert(q.text === "", "nothing leaks into free text");
  // Toggling adds the escaped term and removes it again.
  const added = toggleTagInQuery("", tag);
  assert(added === term + " ", "chip writes the escaped term");
  assert(toggleTagInQuery(added, tag) === "", "and peels it off whole");
  // A backslash in a tag is itself escaped, and round-trips too.
  const bs = "back\\slash tag";
  assertEqual(parseSearchQuery(formatTagTerm(bs) + " ").tags, [bs], "backslash round-trips");
});

run("completeTermInQuery replaces the partial term and leaves the rest", () => {
  assert(
    completeTermInQuery("auth tag:inf", "tag:infra") === "auth tag:infra ",
    "replaces partial",
  );
  assert(completeTermInQuery("tag:", "tag:infra") === "tag:infra ", "fills a bare prefix");
  assert(
    completeTermInQuery("auth ", "tag:infra") === "auth tag:infra ",
    "appends when not in a tag term",
  );
  // A half-typed quoted value — spaces and all — is one term to replace.
  assert(
    completeTermInQuery('auth tag:"in prog', 'tag:"in progress"') === 'auth tag:"in progress" ',
    "replaces a quoted partial",
  );
  // The untagged entry is not a tag, but rides the same path.
  assert(completeTermInQuery("tag:", "is:untagged") === "is:untagged ", "inserts a whole token");
});

run("is:untagged is parsed in its own namespace", () => {
  const q = parseSearchQuery("is:untagged ");
  assert(q.untaggedOnly, "recognised");
  assertEqual(q.tags, [], "not a tag");
  assert(q.text === "", "and not free text either");

  // The whole point of the separate namespace: a tag literally called
  // "untagged" stays addressable, and means something different.
  const t = parseSearchQuery("tag:untagged ");
  assert(!t.untaggedOnly, "tag:untagged is not the untagged filter");
  assertEqual(t.tags, ["untagged"], "it is an ordinary tag");
});

run("is:untagged keeps only conversations with no tags", () => {
  const corpus = [conv("a", ["infra"]), conv("b", []), conv("c", null), conv("d", "[]")];
  const got = filterConversationsByQuery(corpus, { tags: [], untaggedOnly: true });
  assertEqual(
    got.map((c) => c.conversation_id),
    ["b", "c", "d"],
    "malformed and empty payloads all count as untagged",
  );
  // Contradictory query: nothing carries a tag AND no tags.
  assertEqual(
    filterConversationsByQuery(corpus, { tags: ["infra"], untaggedOnly: true }).map(
      (c) => c.conversation_id,
    ),
    [],
    "combining with a tag is empty, as the query says",
  );
});

run("removeTagFromQuery removes only that tag", () => {
  assert(removeTagFromQuery("tag:infra tag:urgent x", "infra") === "tag:urgent x ", "peels one");
  assert(removeTagFromQuery("tag:infra", "infra") === "", "empties fully");
  assert(removeTagFromQuery("tag:Infra", "infra") === "", "folded comparison");
  assert(
    removeTagFromQuery('tag:"in progress" x', "in progress") === "x ",
    "quoted term removed whole",
  );
  assert(
    removeTagFromQuery("tag:infra tag:urg", "infra") === "tag:urg",
    "does not commit a trailing partial tag",
  );
});

run("removeUntaggedFromQuery preserves a trailing partial tag", () => {
  assert(
    removeUntaggedFromQuery("is:untagged tag:urg") === "tag:urg",
    "partial remains uncommitted",
  );
  assert(
    removeUntaggedFromQuery("is:untagged search") === "search ",
    "normal free text keeps the existing trailing-space behavior",
  );
});

run("participant token removal preserves trailing partial terms", () => {
  assert(
    removeUserFromQuery(
      "user:me@example.com is:unattributed tag:infra user:oth",
      "ME@example.com",
    ) === "is:unattributed tag:infra user:oth",
    "removes one user without committing a partial user",
  );
  assert(
    removeUnattributedFromQuery("is:unattributed tag:urg") === "tag:urg",
    "unattributed removal does not commit a partial tag",
  );
  assert(
    removeUnattributedFromQuery("user:me@example.com is:unattributed search") ===
      "user:me@example.com search ",
    "other participant and text terms remain",
  );
});

run("toggleTagInQuery adds then removes", () => {
  const added = toggleTagInQuery("auth", "infra");
  assert(added === "auth tag:infra ", "adds");
  assert(toggleTagInQuery(added, "infra") === "auth ", "removes");
  // A tag with a space round-trips through its quoted form.
  const spaced = toggleTagInQuery("", "in progress");
  assert(spaced === 'tag:"in progress" ', "adds quoted");
  assert(toggleTagInQuery(spaced, "in progress") === "", "removes quoted");
});

run("tagGroupKey is order- and case-insensitive, and null when untagged", () => {
  assert(
    tagGroupKey(conv("a", ["infra", "urgent"])) === tagGroupKey(conv("b", ["urgent", "infra"])),
    "order",
  );
  assert(tagGroupKey(conv("c", ["Infra"])) === tagGroupKey(conv("d", ["infra"])), "case");
  assert(tagGroupKey(conv("e", [])) === null, "untagged has no group");
  assert(
    tagGroupKey(conv("d", ["infra"])) !== tagGroupKey(conv("a", ["infra", "urgent"])),
    "distinct sets differ",
  );
});

run("tagGroupLabel sorts and keeps original casing", () => {
  assert(
    tagGroupLabel(conv("f", ["urgent", "Infra"])) === "#Infra #urgent",
    "sorted, cased, hashed",
  );
  assert(tagGroupLabel(conv("g", ["a", "A"])) === "#a", "folded duplicates collapse");
});

run("tag groups order by first tag, then second", () => {
  const key = (tags: string[]) => tagGroupKey(conv("x", tags))!;
  const got = [key(["b", "d"]), key(["b"]), key(["a", "b"]), key(["b", "c"])]
    .sort(compareTagGroupKeys)
    .map((k) =>
      k
        .split("\u0000")
        .map((t) => `#${t}`)
        .join(" "),
    );
  // A set that is a prefix of another comes first, so the broad #b group sits
  // directly above the narrower groups extending it.
  assertEqual(got, ["#a #b", "#b", "#b #c", "#b #d"], "ordered by tag sequence");
});

run("tag group ordering is total and prefix-aware", () => {
  const key = (tags: string[]) => tagGroupKey(conv("x", tags))!;
  assert(compareTagGroupKeys(key(["b"]), key(["b", "c"])) < 0, "prefix sorts first");
  assert(compareTagGroupKeys(key(["b", "c"]), key(["b"])) > 0, "and is antisymmetric");
  assert(compareTagGroupKeys(key(["a", "z"]), key(["b"])) < 0, "first tag dominates");
  assert(compareTagGroupKeys(key(["a"]), key(["a"])) === 0, "equal keys tie");
});

console.log("\ntagFilter tests passed");

run("is:unread is a state filter, not full-text or a tag", () => {
  const query = parseSearchQuery('IS:UNREAD tag:"in progress" needle');
  assert(query.unreadOnly, "case-insensitive state term");
  assertEqual(query.tags, ["in progress"], "other filters preserved");
  assert(query.text === "needle", "unread never reaches full-text search");
  assert(parseSearchQuery("is:unread").unreadOnly, "no trailing space required");
  assert(!parseSearchQuery('tag:"is:unread" ').unreadOnly, "quoted tag is not unread");
});

run("unread toggle preserves text, tags, and unfinished structured terms", () => {
  assert(toggleUnreadInQuery("") === "is:unread ", "adds unread");
  assert(toggleUnreadInQuery("tag:inf") === "is:unread tag:inf", "does not commit a prefix");
  assert(toggleUnreadInQuery("is:unread tag:inf") === "tag:inf", "removes without committing");
  assert(toggleUnreadInQuery("IS:UNREAD is:unread") === "", "removes every unread term");
  assert(
    toggleUnreadInQuery('is:unread tag:"is:unread" needle') === 'tag:"is:unread" needle ',
    "preserves quoted values and free text",
  );
});
