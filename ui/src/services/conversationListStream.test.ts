import { applyConversationListPatch } from "./conversationListStream";
import type { ConversationWithState } from "../types";

function conv(id: string, slug: string, working = false): ConversationWithState {
  return {
    conversation_id: id,
    slug,
    user_initiated: true,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    cwd: null,
    archived: false,
    parent_conversation_id: null,
    model: null,
    conversation_options: "{}",
    current_generation: 0,
    agent_working: working,
    working,
    subagent_count: 0,
  };
}

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(`Assertion failed: ${message}`);
}

function run(name: string, fn: () => void): void {
  try {
    fn();
    console.log(`✓ ${name}`);
  } catch (err) {
    console.error(`✗ ${name}`);
    throw err;
  }
}

run("replace root", () => {
  const next = applyConversationListPatch(
    [],
    [{ op: "replace", path: "", value: [conv("a", "alpha")] }],
  );
  assert(next.length === 1, "expected one conversation");
  assert(next[0].slug === "alpha", "expected replaced root value");
});

run("add, replace field, remove", () => {
  let state = [conv("a", "alpha")];
  state = applyConversationListPatch(state, [{ op: "add", path: "/0", value: conv("b", "beta") }]);
  assert(state.map((c) => c.conversation_id).join(",") === "b,a", "expected inserted item");

  state = applyConversationListPatch(state, [{ op: "replace", path: "/1/working", value: true }]);
  assert(state[1].working, "expected field replacement");

  state = applyConversationListPatch(state, [{ op: "remove", path: "/0" }]);
  assert(state.length === 1 && state[0].conversation_id === "a", "expected removal");
});

run("move", () => {
  const state = applyConversationListPatch(
    [conv("a", "alpha"), conv("b", "beta")],
    [{ op: "move", from: "/1", path: "/0" }],
  );
  assert(state.map((c) => c.conversation_id).join(",") === "b,a", "expected moved item");
});

run("json pointer escaping", () => {
  const state = applyConversationListPatch(
    [{ ...conv("a", "alpha"), git_subject: "old" }],
    [{ op: "replace", path: "/0/git_subject", value: "slash / tilde ~ ok" }],
  );
  assert(state[0].git_subject === "slash / tilde ~ ok", "expected escaped path support");
});

console.log("\nConversationListStream tests passed");
