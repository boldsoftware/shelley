import assert from "node:assert/strict";
import type { Message } from "../types";
import {
  isDeliveredUserMessage,
  isFailedChatDelivery,
  isHumanUserMessage,
  isTypedUserMessage,
  isVisibleConversationMessage,
} from "./conversationView";

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

for (const type of ["error", "warning", "gitinfo", "modelchange", "profileturn"] as const) {
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

// Brief view: what the user typed, and problems; not the agent's output nor
// messages from subagents or jobs.
const fromSubagent = message({
  message_id: "from-subagent",
  type: "user",
  user_data: JSON.stringify({ sender_conversation_id: "child" }),
  llm_data: JSON.stringify({ Role: 0, Content: [{ Type: 2, Text: "report" }] }),
});
const carried = message({
  message_id: "carried",
  type: "user",
  user_data: JSON.stringify({ compaction_carried: "true" }),
  llm_data: JSON.stringify({ Role: 0, Content: [{ Type: 2, Text: "hello" }] }),
});
assert.equal(isTypedUserMessage(human), true);
assert.equal(isTypedUserMessage(carried), true);
const live = message({
  ...human,
  message_id: "live",
  user_data: JSON.stringify({ live_delivery_id: "delivery-1" }),
});
assert.equal(isTypedUserMessage(live), true);
assert.equal(isVisibleConversationMessage(live, "brief"), true);
assert.equal(
  isTypedUserMessage({
    ...live,
    message_id: "carried-live",
    user_data: JSON.stringify({ live_delivery_id: "delivery-1", compaction_carried: "true" }),
  }),
  true,
);
assert.equal(
  isTypedUserMessage({
    ...live,
    message_id: "machine-live",
    user_data: JSON.stringify({ live_delivery_id: "delivery-1", sender_conversation_id: "child" }),
  }),
  false,
);
assert.equal(
  isTypedUserMessage({
    ...carried,
    message_id: "carried-again",
    user_data: JSON.stringify({ compaction_carried: "true", carried_from_sequence_id: "3" }),
  }),
  true,
);
// A compaction carries an in-place compaction's summary as a user message.
assert.equal(
  isTypedUserMessage({
    ...carried,
    message_id: "squish-note",
    user_data: JSON.stringify({ compaction_carried: "true", squish_note: "s5.0" }),
  }),
  false,
);
assert.equal(isTypedUserMessage(fromSubagent), false);
assert.equal(isVisibleConversationMessage(human, "brief"), true);
assert.equal(isVisibleConversationMessage(fromSubagent, "brief"), false);
assert.equal(isVisibleConversationMessage(final, "brief"), false);
assert.equal(isVisibleConversationMessage(toolResult, "brief"), false);
assert.equal(
  isVisibleConversationMessage(message({ message_id: "error", type: "error" }), "brief"),
  true,
);
assert.equal(
  isVisibleConversationMessage(message({ message_id: "gitinfo", type: "gitinfo" }), "brief"),
  false,
);

const call = {
  toolName: "message_user",
  toolInput: { text: "hi" },
  hasResult: true,
  toolError: false,
  display: {},
};
assert.equal(isDeliveredUserMessage(call), true);
assert.equal(isDeliveredUserMessage({ ...call, hasResult: false }), false);
assert.equal(isDeliveredUserMessage({ ...call, toolError: true }), false);
assert.equal(isDeliveredUserMessage({ ...call, toolName: "bash" }), false);
assert.equal(
  isDeliveredUserMessage({ ...call, toolInput: { reaction: "👍", reply_to: 2 } }),
  false,
);
assert.equal(
  isDeliveredUserMessage({
    ...call,
    toolInput: { attachments: ["a.txt"] },
    display: { attachments: [{ path: "/a.txt", name: "a.txt", size: 1 }] },
  }),
  true,
);

const refused = { ...call, toolError: true, display: { chat_failed: true } };
assert.equal(isFailedChatDelivery(refused), true);
assert.equal(isDeliveredUserMessage(refused), false);
assert.equal(isFailedChatDelivery({ ...refused, display: {} }), false);
assert.equal(isFailedChatDelivery({ ...refused, hasResult: false }), false);
assert.equal(isFailedChatDelivery(call), false);

console.log("conversationView tests passed");
