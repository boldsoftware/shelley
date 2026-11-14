import {
  activeStructuredQueryEdit,
  clearConversationQueryText,
  completeStructuredQueryEdit,
  omitStructuredQueryEdit,
  rebaseConversationQuerySelection,
  removeConversationQueryTerm,
  structuredQueryEditAtOffset,
  tokenizeConversationQuery,
} from "./conversationQuery";

function assert(condition: boolean, message: string): asserts condition {
  if (!condition) throw new Error(`Assertion failed: ${message}`);
}

function assertEqual(actual: unknown, expected: unknown, message: string): void {
  const got = JSON.stringify(actual);
  const want = JSON.stringify(expected);
  if (got !== want) throw new Error(`Assertion failed: ${message} (got ${got}, want ${want})`);
}

function run(name: string, fn: () => void): void {
  try {
    fn();
    console.log(`\u2713 ${name}`);
  } catch (error) {
    console.error(`\u2717 ${name}`);
    throw error;
  }
}

run("tokenizes interleaved text and committed terms in lexical order", () => {
  const raw = "test user:aaa@x.com tag:aaa big";
  const tokens = tokenizeConversationQuery(raw);
  assertEqual(
    tokens.map((token) => [token.kind, token.raw]),
    [
      ["text", "test "],
      ["user", "user:aaa@x.com"],
      ["text", " "],
      ["tag", "tag:aaa"],
      ["text", " big"],
    ],
    "ordered tokens",
  );
  assert(tokens.map((token) => token.raw).join("") === raw, "serialization is exact");
});

run("preserves quoted tag syntax while displaying a literal tag label", () => {
  const raw = 'before  tag:"in progress"  after';
  const tokens = tokenizeConversationQuery(raw);
  const tag = tokens.find((token) => token.kind === "tag");
  assert(tag?.kind === "tag", "tag token found");
  assert(tag?.raw === 'tag:"in progress"', "raw quoted term is retained");
  assert(tag?.label === "tag:in progress", "pill label omits query quotes");
  assert(tokens.map((token) => token.raw).join("") === raw, "spacing and quotes round-trip");
});

run("keeps a trailing tag or user prefix editable", () => {
  assertEqual(
    tokenizeConversationQuery("tag:").map((token) => token.kind),
    ["text"],
    "bare tag prefix",
  );
  assertEqual(
    tokenizeConversationQuery("user:aaa@x.com").map((token) => token.kind),
    ["text"],
    "trailing user prefix",
  );
  assertEqual(
    tokenizeConversationQuery("tag:aaa ").map((token) => token.kind),
    ["text", "tag", "text"],
    "space commits a tag",
  );
});

run("state terms are atomic even at the end of a query", () => {
  assertEqual(
    tokenizeConversationQuery("IS:UNREAD").map((token) => token.kind),
    ["text", "unread", "text"],
    "unread state",
  );
  assertEqual(
    tokenizeConversationQuery("is:untagged").map((token) => token.kind),
    ["text", "untagged", "text"],
    "untagged state",
  );
  assertEqual(
    tokenizeConversationQuery("is:unattributed").map((token) => token.kind),
    ["text", "unattributed", "text"],
    "unattributed state",
  );
});

run("atomic removal removes one pill and an adjoining separator", () => {
  const raw = "test user:aaa@x.com tag:aaa big";
  const tokens = tokenizeConversationQuery(raw);
  const user = tokens.find((token) => token.kind === "user");
  const tag = tokens.find((token) => token.kind === "tag");
  assert(user?.kind === "user", "user token found");
  assert(tag?.kind === "tag", "tag token found");
  assert(
    removeConversationQueryTerm(raw, user.start, user.end).query === "test tag:aaa big",
    "middle user removed whole",
  );
  assert(
    removeConversationQueryTerm(raw, tag.start, tag.end).query === "test user:aaa@x.com big",
    "middle tag removed whole",
  );
});

run("atomic removal handles edge pills", () => {
  const first = "tag:aaa text";
  assert(
    removeConversationQueryTerm(first, 0, 7).query === "text",
    "leading pill and following separator",
  );
  const last = "text user:aaa@x.com ";
  const user = tokenizeConversationQuery(last).find((token) => token.kind === "user");
  assert(user?.kind === "user", "trailing user found");
  assert(
    removeConversationQueryTerm(last, user.start, user.end).query === "text ",
    "trailing pill and separator",
  );
});

run("tracks and completes an unwrapped structured term at its lexical range", () => {
  const raw = "left tag:alpha right user:kept@example.com ";
  const edit = { kind: "tag" as const, start: 5, end: 14 };
  assertEqual(
    activeStructuredQueryEdit(raw, edit),
    { ...edit, prefix: "alpha" },
    "active tag prefix",
  );
  assert(
    omitStructuredQueryEdit(raw, edit) === "left  right user:kept@example.com ",
    "parsing can omit only the active term",
  );
  const userEdit = { kind: "user" as const, start: 21, end: 42 };
  assertEqual(
    activeStructuredQueryEdit(raw, userEdit),
    { ...userEdit, prefix: "kept@example.com" },
    "active user prefix",
  );
});

run("finds an editable structured term at a raw caret offset", () => {
  const raw = 'left tag:"in progress" right user:person@example.com ';
  assertEqual(
    structuredQueryEditAtOffset(raw, 12),
    { kind: "tag", start: 5, end: 22, prefix: "in progress" },
    "quoted middle tag",
  );
  assertEqual(
    structuredQueryEditAtOffset(raw, 52),
    { kind: "user", start: 29, end: 52, prefix: "person@example.com" },
    "end edge belongs to user term",
  );
  assert(structuredQueryEditAtOffset(raw, 2) === null, "ordinary text has no structured edit");
});

run("completion commits a trailing partial but replaces a middle term exactly", () => {
  assertEqual(
    completeStructuredQueryEdit("find tag:al", { kind: "tag", start: 5, end: 11 }, "tag:alpha"),
    { query: "find tag:alpha ", caret: 15 },
    "trailing completion adds a separator",
  );
  assertEqual(
    completeStructuredQueryEdit(
      "left tag:al right",
      { kind: "tag", start: 5, end: 11 },
      "tag:alpha",
    ),
    { query: "left tag:alpha right", caret: 15 },
    "middle completion keeps surrounding text",
  );
});

run("rebases native selections through external query changes", () => {
  assertEqual(
    rebaseConversationQuerySelection("left right", "new left right", {
      anchor: 10,
      focus: 10,
    }),
    { anchor: 14, focus: 14 },
    "a caret follows text inserted before it",
  );
  assertEqual(
    rebaseConversationQuerySelection("left right", "left broad right", {
      anchor: 5,
      focus: 10,
    }),
    { anchor: 5, focus: 16 },
    "a forward selection keeps its direction around replacement text",
  );
  assertEqual(
    rebaseConversationQuerySelection("left right", "left broad right", {
      anchor: 10,
      focus: 5,
    }),
    { anchor: 16, focus: 5 },
    "a backward selection keeps its direction",
  );
});

run("clears editable text without losing earlier committed pills", () => {
  assert(
    clearConversationQueryText("tag:kept tag:", { kind: "tag", start: 9, end: 13 }) === "tag:kept ",
    "trailing partial is removed after a committed pill",
  );
  assert(
    clearConversationQueryText("left tag:editing user:kept@example.com ", {
      kind: "tag",
      start: 5,
      end: 16,
    }) === "user:kept@example.com ",
    "a degraded middle term is excluded",
  );
});

run("removes an explicitly selected modifier range", () => {
  const raw = "left tag: right";
  const edit = { kind: "tag" as const, start: 5, end: 9 };
  assertEqual(
    removeConversationQueryTerm(raw, edit.start, edit.end),
    { query: "left right", caret: 5 },
    "bare modifier and one separator are removed",
  );
});

console.log("\nconversationQuery tests passed");
