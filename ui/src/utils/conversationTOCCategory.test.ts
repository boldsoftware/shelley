import assert from "node:assert/strict";
import type { LLMContent, Message } from "../types";
import { messageTOCCategory, toolTOCCategory } from "./conversationTOCCategory";

let sequence = 0;
const text: LLMContent[] = [{ ID: "", Type: 2, Text: "A message" }];
function category(type: Message["type"], overrides: Partial<Message> = {}, content = text) {
  const id = ++sequence;
  const message: Message = {
    message_id: `toc-message-${id}`,
    conversation_id: "conversation",
    sequence_id: id,
    type,
    created_at: "2026-10-09T00:00:00Z",
    generation: 1,
    llm_data: JSON.stringify({ Role: type === "agent" ? 1 : 0, Content: content }),
    ...overrides,
  };
  return messageTOCCategory(message, content);
}

assert.equal(category("user"), "user");
assert.equal(category("user", { user_data: "not json" }), "user");
const carried = { compaction_carried: "true", carried_from_sequence_id: "3" };
assert.equal(category("user", { user_data: JSON.stringify(carried) }), "user");
for (const relationship of ["subagent", "parent"]) {
  assert.equal(
    category("user", {
      user_data: JSON.stringify({
        ...carried,
        sender_relationship: relationship,
        sender_conversation_id: "other-conversation",
        sender_slug: "other-agent",
      }),
    }),
    "subagent",
  );
}
assert.equal(
  category("user", { user_data: JSON.stringify({ background_job_id: "job-1" }) }),
  "background",
);
for (const metadata of [
  { distilled: "true" },
  { cwd_change: true, from: "/old", to: "/new" },
  { context_nudge: true },
  { mcp_server_change: "added", server_name: "test" },
  { sender_conversation_id: "incomplete-provenance" },
]) {
  assert.equal(category("user", { user_data: JSON.stringify(metadata) }), "notice");
}
assert.equal(category("agent", { end_of_turn: true }), "reply");
assert.equal(category("agent", { end_of_turn: false }), "progress");
assert.equal(
  category("agent", {
    end_of_turn: true,
    user_data: JSON.stringify({ distill_status: "complete" }),
  }),
  "notice",
);
for (const Type of [6, 8]) {
  assert.equal(category("user", {}, [...text, { ID: "", Type }]), "tool-result");
}

const call: LLMContent = { ID: "call", Type: 5, ToolName: "read_image" };
assert.equal(toolTOCCategory(call), "tool-result");
for (const end_turn of [false, true]) {
  const chatCall = { ...call, ToolName: "message_user", ToolInput: { end_turn } };
  assert.equal(toolTOCCategory(chatCall), end_turn ? "reply" : "progress");
}

console.log("conversationTOCCategory tests passed");
