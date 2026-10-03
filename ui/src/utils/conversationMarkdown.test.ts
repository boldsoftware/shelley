// Unit tests for conversationToMarkdown.
// Run with: tsx src/utils/conversationMarkdown.test.ts
import { conversationToMarkdown } from "./conversationMarkdown";
import { Conversation, Message } from "../types";

function msg(partial: Partial<Message>): Message {
  return {
    message_id: "m",
    conversation_id: "c",
    sequence_id: 0,
    type: "agent",
    created_at: new Date().toISOString(),
    generation: 1,
    ...partial,
  } as Message;
}

// A minimal llm_data blob with the given content blocks.
function llm(content: unknown[]): string {
  return JSON.stringify({ Content: content });
}

const conversation: Conversation = {
  conversation_id: "c",
  slug: "my-chat",
  created_at: "2026-01-01T00:00:00Z",
  cwd: "/tmp/work",
  model: "claude-opus-4.8",
} as Conversation;

let passed = 0;
let failed = 0;
const failures: string[] = [];

function check(name: string, cond: boolean, detail = "") {
  if (cond) passed++;
  else {
    failed++;
    failures.push(`FAIL: ${name}${detail ? "\n  " + detail : ""}`);
  }
}

// --- Header / metadata ---
{
  const md = conversationToMarkdown(conversation, []);
  check("header title", md.includes("# my-chat"), md);
  check("header model", md.includes("claude-opus-4.8"));
  check("header cwd", md.includes("/tmp/work"));
}

// --- User + assistant text ---
{
  const messages: Message[] = [
    msg({ type: "user", llm_data: llm([{ Type: 2, Text: "Hello there" }]) }),
    msg({ type: "agent", llm_data: llm([{ Type: 2, Text: "Hi! How can I help?" }]) }),
  ];
  const md = conversationToMarkdown(conversation, messages);
  check("user heading", md.includes("User"), md);
  check("user text", md.includes("Hello there"));
  check("assistant heading", md.includes("Assistant"));
  check("assistant text", md.includes("Hi! How can I help?"));
}

// --- Thinking blocks, toggled by option ---
{
  const messages: Message[] = [
    msg({
      type: "agent",
      llm_data: llm([
        { Type: 3, Thinking: "secret plan" },
        { Type: 2, Text: "done" },
      ]),
    }),
  ];
  const withThinking = conversationToMarkdown(conversation, messages, { includeThinking: true });
  check("thinking included", withThinking.includes("secret plan"), withThinking);
  const without = conversationToMarkdown(conversation, messages, { includeThinking: false });
  check("thinking excluded", !without.includes("secret plan"));
}

// --- Tool use + result rendered as fenced blocks ---
{
  const messages: Message[] = [
    msg({
      type: "agent",
      llm_data: llm([{ Type: 5, ID: "t1", ToolName: "bash", ToolInput: { command: "ls" } }]),
    }),
    msg({
      type: "user",
      llm_data: llm([{ Type: 6, ToolUseID: "t1", ToolResult: [{ Type: 2, Text: "file.txt" }] }]),
    }),
  ];
  const md = conversationToMarkdown(conversation, messages);
  check("tool name", md.includes("`bash`"), md);
  check("tool input fenced", md.includes('"command"') && md.includes("ls"));
  check("tool result", md.includes("file.txt"));
  check("tool result collapsible", md.includes("<details>"));

  // Excluding tool outputs drops the result body but keeps the call + input.
  const noOutputs = conversationToMarkdown(conversation, messages, {
    includeToolOutputs: false,
  });
  check("tool output excluded", !noOutputs.includes("file.txt"), noOutputs);
  check("tool call still present", noOutputs.includes("`bash`"), noOutputs);
  check("tool input still present", noOutputs.includes("ls"), noOutputs);
}

// --- Tool-result-carrying user messages don't get a User heading ---
{
  const messages: Message[] = [
    msg({
      type: "user",
      llm_data: llm([{ Type: 6, ToolUseID: "t1", ToolResult: [{ Type: 2, Text: "out" }] }]),
    }),
  ];
  const md = conversationToMarkdown(conversation, messages);
  check("no spurious user heading", !md.includes("## User"), md);
}

// --- Error messages surface their text ---
{
  const messages: Message[] = [
    msg({ type: "error", user_data: JSON.stringify({ text: "boom happened" }) }),
  ];
  const md = conversationToMarkdown(conversation, messages);
  check("error text", md.includes("boom happened"), md);
}

// --- Fence escaping: body containing ``` gets a longer fence ---
{
  const messages: Message[] = [
    msg({
      type: "agent",
      llm_data: llm([{ Type: 5, ID: "t1", ToolName: "shell", ToolInput: "```nested```" }]),
    }),
  ];
  const md = conversationToMarkdown(conversation, messages);
  check("fence escapes backticks", md.includes("````"), md);
}

// --- Agent-to-agent messages must never masquerade as human input. ---
for (const relationship of ["subagent", "parent"]) {
  const messages = [
    msg({
      type: "user",
      user_data: JSON.stringify({
        sender_conversation_id: "child",
        sender_slug: "audit-symmetric",
        sender_relationship: relationship,
      }),
      llm_data: llm([{ Type: 2, Text: "Symmetric early leads saved" }]),
    }),
  ];
  const md = conversationToMarkdown(conversation, messages);
  const label = relationship === "subagent" ? "Subagent" : "Parent conversation";
  check(
    `${relationship} attributed in export`,
    md.includes(`## ${label}: audit-symmetric`) &&
      md.includes("Symmetric early leads saved") &&
      !md.includes("## User"),
  );
}

console.log(`\nconversationToMarkdown Tests: ${passed} passed, ${failed} failed\n`);
if (failures.length > 0) {
  for (const f of failures) console.log(f);
  process.exit(1);
}
console.log("All tests passed!");
process.exit(0);
