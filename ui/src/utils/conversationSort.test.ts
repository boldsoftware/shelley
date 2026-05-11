import { BUCKET_MS, sortConversationsByBucket, updatedBucket } from "./conversationSort";
import type { ConversationWithState } from "../types";

function conv(id: string, updatedAt: string): ConversationWithState {
  return {
    conversation_id: id,
    slug: id,
    user_initiated: true,
    created_at: updatedAt,
    updated_at: updatedAt,
    cwd: null,
    archived: false,
    parent_conversation_id: null,
    model: null,
    conversation_options: "{}",
    current_generation: 0,
    agent_working: false,
    working: false,
    subagent_count: 0,
  };
}

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

run("groups updates within the same 5-minute bucket", () => {
  // Two conversations updated within seconds of each other should not flip
  // ordering: ULID A is older (lexicographically smaller) so B sorts first.
  const a = conv("01HZZZZZZZZZZZZZZZZZZZZZZA", "2026-05-10T12:00:30Z");
  const b = conv("01HZZZZZZZZZZZZZZZZZZZZZZB", "2026-05-10T12:00:35Z");
  let order = sortConversationsByBucket([a, b]).map((c) => c.conversation_id);
  assert(order[0].endsWith("B") && order[1].endsWith("A"), `expected B,A got ${order}`);
  // Now flip which one was updated more recently within the same bucket.
  // Order should be unchanged (B still first by ULID tie-break).
  const a2 = { ...a, updated_at: "2026-05-10T12:01:10Z" };
  order = sortConversationsByBucket([a2, b]).map((c) => c.conversation_id);
  assert(order[0].endsWith("B") && order[1].endsWith("A"), `flip-flop: ${order}`);
});

run("different buckets sort by recency", () => {
  const a = conv("01HZZZZZZZZZZZZZZZZZZZZZZA", "2026-05-10T12:00:00Z");
  const b = conv("01HZZZZZZZZZZZZZZZZZZZZZZB", "2026-05-10T12:10:00Z");
  const order = sortConversationsByBucket([a, b]).map((c) => c.conversation_id);
  assert(order[0].endsWith("B"), `expected newer bucket first, got ${order}`);
});

run("bucket boundaries", () => {
  const t1 = updatedBucket("2026-05-10T12:00:00Z");
  const t2 = updatedBucket("2026-05-10T12:04:59Z");
  const t3 = updatedBucket("2026-05-10T12:05:00Z");
  assert(t1 === t2, "same bucket within 5 minutes");
  assert(t3 === t1 + 1, "crossing the boundary advances bucket");
  assert(BUCKET_MS === 300000, "5 minute bucket");
});

run("does not mutate input", () => {
  const input = [
    conv("01HZZZZZZZZZZZZZZZZZZZZZZA", "2026-05-10T12:00:00Z"),
    conv("01HZZZZZZZZZZZZZZZZZZZZZZB", "2026-05-10T12:10:00Z"),
  ];
  const before = input.map((c) => c.conversation_id).join(",");
  sortConversationsByBucket(input);
  assert(input.map((c) => c.conversation_id).join(",") === before, "input mutated");
});

console.log("\nconversationSort tests passed");
