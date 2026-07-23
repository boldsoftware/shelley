import { currentTurnAssistantPreview } from "./assistantTurnPreview";

function check(name: string, actual: unknown, expected: unknown) {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(`${name}: got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);
  }
}

const strip = (_id: string, text: string) => text.replace(/\s+/g, " ").trim();

function agent(id: string, text: string) {
  return {
    type: "agent",
    message_id: id,
    llm_data: JSON.stringify({
      Content: [{ Type: 2, Text: text }],
    }),
  };
}

function user(id: string) {
  return { type: "user", message_id: id, llm_data: "{}" };
}

check(
  "uses only current-turn agent text",
  currentTurnAssistantPreview(
    [
      user("u1"),
      agent("a1", "Done. Here's the summary: Moved /claw.md"),
      user("u2"),
      agent("a2", "You're right — I didn't use Grok."),
    ],
    strip,
  ),
  "You're right — I didn't use Grok.",
);

check(
  "does not fall back to previous turn when current turn has no text yet",
  currentTurnAssistantPreview(
    [user("u1"), agent("a1", "Done. Here's the summary: Moved /claw.md"), user("u2")],
    strip,
  ),
  "",
);

check(
  "skips tool-only agent messages and finds later text in turn",
  currentTurnAssistantPreview(
    [
      user("u1"),
      agent("a0", "old turn"),
      user("u2"),
      { type: "agent", message_id: "toolish", llm_data: JSON.stringify({ Content: [{ Type: 3, ToolName: "bash" }] }) },
      agent("a2", "Final answer."),
    ],
    strip,
  ),
  "Final answer.",
);

{
  const truncated = currentTurnAssistantPreview(
    [user("u1"), agent("a1", "x".repeat(200))],
    strip,
    20,
  );
  check("truncates long previews length", truncated.length <= 20, true);
  check("truncates with ellipsis", truncated.endsWith("…"), true);
}

check("empty messages", currentTurnAssistantPreview([], strip), "");

console.log("assistantTurnPreview tests passed");
