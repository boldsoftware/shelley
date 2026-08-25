import { subagentActivity } from "./subagentActivity";
import type { Message } from "../types";

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

function agentMsg(content: unknown[], seq = 1): Message {
  return {
    message_id: `m${seq}`,
    conversation_id: "c1",
    sequence_id: seq,
    type: "agent",
    llm_data: JSON.stringify({ Content: content }),
    created_at: "2024-01-01T00:00:00Z",
    generation: 1,
  } as Message;
}

run("empty inputs yield empty activity", () => {
  assert(subagentActivity({}) === "", "expected empty");
});

run("streaming text wins and yields last non-empty line", () => {
  const got = subagentActivity({
    streamingText: "first line\nsecond line\n\n",
    preview: "old preview",
  });
  assert(got === "second line", `got ${JSON.stringify(got)}`);
});

run("tool progress tail used when no streaming text", () => {
  const got = subagentActivity({
    toolProgress: {
      t1: { tool_use_id: "t1", tool_name: "bash", output: "building...\ndone stage 1\n" },
    },
    preview: "old preview",
  });
  assert(got === "\u{1F6E0}\uFE0F done stage 1", `got ${JSON.stringify(got)}`);
});

run("last tool_use in messages renders emoji + headline", () => {
  const got = subagentActivity({
    messages: [
      agentMsg([{ ID: "x", Type: 2, Text: "thinking about it" }], 1),
      agentMsg(
        [{ ID: "t9", Type: 5, ToolName: "bash", ToolInput: { command: "go test ./..." } }],
        2,
      ),
    ],
  });
  assert(got.includes("go test"), `got ${JSON.stringify(got)}`);
  assert(
    got.startsWith("\u{1F6E0}\uFE0F"),
    `expected bash emoji prefix, got ${JSON.stringify(got)}`,
  );
});

run("agent text tail used when last message has no tool_use", () => {
  const got = subagentActivity({
    messages: [agentMsg([{ ID: "x", Type: 2, Text: "working on the fix\nalmost there" }], 1)],
  });
  assert(got === "almost there", `got ${JSON.stringify(got)}`);
});

run("falls back to preview", () => {
  const got = subagentActivity({ preview: "summary of last agent message" });
  assert(got === "summary of last agent message", `got ${JSON.stringify(got)}`);
});

run("skips unparseable llm_data and keeps scanning", () => {
  const bad = {
    message_id: "mBad",
    conversation_id: "c1",
    sequence_id: 3,
    type: "agent",
    llm_data: "{not json",
    created_at: "2024-01-01T00:00:00Z",
    generation: 1,
  } as Message;
  const got = subagentActivity({
    messages: [agentMsg([{ ID: "x", Type: 2, Text: "real content" }], 1), bad],
  });
  assert(got === "real content", `got ${JSON.stringify(got)}`);
});

console.log("subagentActivity tests passed");
