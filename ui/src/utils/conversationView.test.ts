import assert from "node:assert/strict";
import type { Message } from "../types";
import { isHumanUserMessage, isVisibleConversationMessage } from "./conversationView";

function message(overrides: Partial<Message>): Message {
  return {
    message_id: overrides.message_id || crypto.randomUUID(),
    conversation_id: "conversation",
    sequence_id: 1,
    type: "agent",
    created_at: "2026-08-06T00:00:00Z",
    generation: 1,
    ...overrides,
  };
}

const human = message({
  message_id: "human",
  type: "user",
  llm_data: JSON.stringify({ Role: 0, Content: [{ Type: 2, Text: "hello" }] }),
});
const toolResult = message({
  message_id: "tool-result",
  type: "user",
  llm_data: JSON.stringify({ Role: 0, Content: [{ Type: 6, ToolUseID: "tool-1" }] }),
});
const distilledSummary = message({
  message_id: "distilled-summary",
  type: "user",
  user_data: JSON.stringify({ distilled: "true" }),
  llm_data: JSON.stringify({ Role: 0, Content: [{ Type: 2, Text: "summary" }] }),
});
const intermediate = message({ message_id: "intermediate", type: "agent", end_of_turn: false });
const final = message({ message_id: "final", type: "agent", end_of_turn: true });

assert.equal(isHumanUserMessage(human), true);
assert.equal(isHumanUserMessage(toolResult), false);
assert.equal(isHumanUserMessage(distilledSummary), false);
assert.equal(isVisibleConversationMessage(intermediate, "all"), true);
assert.equal(isVisibleConversationMessage(toolResult, "all"), true);
assert.equal(isVisibleConversationMessage(human, "end-of-turn"), true);
assert.equal(isVisibleConversationMessage(toolResult, "end-of-turn"), false);
assert.equal(isVisibleConversationMessage(distilledSummary, "end-of-turn"), false);
assert.equal(isVisibleConversationMessage(intermediate, "end-of-turn"), false);
assert.equal(isVisibleConversationMessage(final, "end-of-turn"), true);

for (const type of ["error", "warning", "gitinfo", "modelchange"] as const) {
  assert.equal(
    isVisibleConversationMessage(message({ message_id: type, type }), "end-of-turn"),
    true,
  );
}

assert.equal(
  isVisibleConversationMessage(
    message({
      message_id: "distill-status",
      type: "agent",
      user_data: JSON.stringify({ distill_status: "complete" }),
    }),
    "end-of-turn",
  ),
  true,
);

assert.equal(
  isVisibleConversationMessage(message({ message_id: "system", type: "system" }), "end-of-turn"),
  false,
);

console.log("conversationView tests passed");
