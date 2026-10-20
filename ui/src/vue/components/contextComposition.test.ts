import type { Message } from "../../types";
import { contextCategories, promptGrowth, rewarmUsd } from "./contextCategories";
import { contextCompositionPoints } from "./contextComposition";

let failed = 0;
function assert(cond: boolean, msg: string) {
  if (!cond) {
    failed++;
    console.error(`FAIL: ${msg}`);
  }
}

let seq = 0;
function msg(type: string, content: object[], extra: Partial<Message> = {}): Message {
  seq++;
  return {
    message_id: `m${seq}`,
    conversation_id: "c",
    sequence_id: seq,
    type,
    generation: 1,
    created_at: "",
    llm_data: JSON.stringify({ Content: content }),
    ...extra,
  } as unknown as Message;
}
const usage = (tokens: number) => ({ usage_data: JSON.stringify({ input_tokens: tokens }) });
const big = "x".repeat(40_000); // ~10k tokens

const messages = [
  msg("user", [{ Type: 2, Text: "read it" }]),
  msg(
    "agent",
    [{ Type: 5, ID: "t1", ToolName: "bash", ToolInput: { command: "cat f" } }],
    usage(100),
  ),
  msg("user", [{ Type: 6, ToolUseID: "t1", ToolResult: [{ Type: 2, Text: big }] }]),
  msg("user", [{ Type: 2, Text: "Context is 10k." }]),
  msg("agent", [{ Type: 5, ID: "t2", ToolName: "compact_in_place", ToolInput: {} }], usage(10_100)),
];
messages.push(
  {
    ...msg("inplacecompaction", []),
    llm_data: undefined,
    user_data: JSON.stringify({
      trims: [{ sequence_id: 3, tool_use_id: "t1" }],
      hidden_sequence_ids: [4],
    }),
  } as unknown as Message,
  msg("user", [{ Type: 6, ToolUseID: "t2", ToolResult: [{ Type: 2, Text: "Compacted." }] }]),
  msg("agent", [{ Type: 2, Text: "done" }], usage(150)),
);

const points = contextCompositionPoints(messages);
assert(points.length === 3, `points = ${points.length}`);
const [, before, after] = points;
assert(after.segment !== before.segment, "in-place compaction starts a new segment");
assert(before.segment === points[0].segment, "no compaction, same segment");
const fileRead = (p: (typeof points)[number]) => p.parts["bash:file read"] || 0;
assert(fileRead(before) > 9_000, `file read before = ${fileRead(before)}`);
assert(fileRead(after) < 1_000, `trimmed file read after = ${fileRead(after)}`);
assert(!JSON.stringify(after.toolBreakdown).includes("-"), "no negative breakdown");
const sum = Object.values(after.parts).reduce((a, b) => a + b, 0);
assert(Math.abs(sum - 150) <= 5, `after parts sum to the reported total: ${sum}`);

// A call's new prompt tokens are the previous call's response and the tool
// results it got back, not the call's own response.
{
  const out = (text: string, tool: string, command: string) => [
    ...(text ? [{ Type: 2, Text: text }] : []),
    ...(tool ? [{ Type: 5, ID: tool, ToolName: "bash", ToolInput: { command } }] : []),
  ];
  const result = (tool: string, text: string) =>
    msg("user", [{ Type: 6, ToolUseID: tool, ToolResult: [{ Type: 2, Text: text }] }]);
  const points = contextCompositionPoints([
    msg("user", [{ Type: 2, Text: "go" }]),
    msg("agent", out(big, "r1", "cat f"), usage(10_100)),
    result("r1", "y".repeat(4_000)),
    msg("agent", out("", "r2", "cat g"), usage(11_200)),
    result("r2", big),
    msg("agent", out("z".repeat(80_000), "", ""), usage(41_300)),
  ]);
  const cats = contextCategories(points);
  const label = (i: number) => promptGrowth(points, i, cats)?.label;
  assert(label(1) === "text", `after a long reply and a short result: ${label(1)}`);
  assert(label(2) === "bash · file read", `after a long result, not its own reply: ${label(2)}`);
  assert(promptGrowth(points, 0, cats) === null, "the first call has nothing to grow from");
}

{
  // Rewarming a cold cache re-writes the whole context at the cache-write price.
  const points = contextCompositionPoints([
    msg("user", [{ Type: 2, Text: big }]),
    msg("agent", [{ Type: 2, Text: "ok" }], {
      usage_data: JSON.stringify({ input_tokens: 2_000_000, model: "m1" }),
    }),
  ]);
  const [point] = points;
  assert(point.model === "m1", `point model = ${point.model}`);
  const costs = { m1: { input: 3, output: 15, cache_read: 0.3, cache_write: 3.75 } };
  assert(rewarmUsd(point, costs) === 7.5, `rewarm = ${rewarmUsd(point, costs)}`);
  const user = rewarmUsd(point, costs, "text");
  assert(user === 7.5, `text rewarm = ${user}`);
  assert(rewarmUsd(point, {}) === null, "unpriced model has no rewarm cost");
}

if (failed) process.exit(1);
console.log("✓ in-place compaction resets the composition");
