import type { Conversation } from "../types";
import { shouldStartDrawerCollapsed } from "./drawerStartup";

function assert(cond: boolean, msg: string): void {
  if (!cond) throw new Error(`Assertion failed: ${msg}`);
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

function conversation(
  id: string,
  options: { parentId?: string | null; isDraft?: boolean } = {},
): Pick<Conversation, "conversation_id" | "parent_conversation_id" | "is_draft"> {
  return {
    conversation_id: id,
    parent_conversation_id: options.parentId ?? null,
    is_draft: options.isDraft ?? false,
  };
}

run("starts collapsed with no saved conversations", () => {
  assert(shouldStartDrawerCollapsed([]), "empty drawer should start collapsed");
});

run("starts collapsed with one conversation", () => {
  assert(
    shouldStartDrawerCollapsed([conversation("only")]),
    "single conversation should start collapsed",
  );
});

run("starts collapsed with only a draft", () => {
  assert(
    shouldStartDrawerCollapsed([conversation("draft", { isDraft: true })]),
    "single draft should start collapsed",
  );
});

run("ignores subagents when deciding whether the drawer is useful", () => {
  assert(
    shouldStartDrawerCollapsed([
      conversation("parent"),
      conversation("subagent", { parentId: "parent" }),
    ]),
    "one top-level conversation plus a subagent should start collapsed",
  );
});

run("starts expanded with multiple top-level conversations", () => {
  assert(
    !shouldStartDrawerCollapsed([conversation("first"), conversation("second")]),
    "multiple conversations should start expanded",
  );
});

console.log("\ndrawerStartup tests passed");
