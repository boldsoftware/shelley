import {
  BUCKET_MS,
  applyStableKeyOrder,
  applyStableOrder,
  sortConversationsByBucket,
  updatedBucket,
} from "./conversationSort";
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

run("applyStableOrder keeps existing items in place and prepends new ones", () => {
  const a = conv("A", "2026-05-10T12:00:00Z");
  const b = conv("B", "2026-05-10T12:01:00Z");
  const c = conv("C", "2026-05-10T12:02:00Z");
  // Initial: empty prev order, items appear in sorted order (newest first).
  const first = applyStableOrder([c, b, a], []);
  assert(first.order.join(",") === "C,B,A", `initial: ${first.order}`);
  // Now A bumps to be newest; sortConversationsByBucket would put A first,
  // but stable order keeps the previous arrangement.
  const sortedAfterBump = [a, c, b]; // pretend sort flipped A to top
  const second = applyStableOrder(sortedAfterBump, first.order);
  assert(second.order.join(",") === "C,B,A", `bumped held: ${second.order}`);
  // A brand-new conversation D appears at the top.
  const d = conv("D", "2026-05-10T12:03:00Z");
  const third = applyStableOrder([d, a, c, b], second.order);
  assert(third.order.join(",") === "D,C,B,A", `new prepended: ${third.order}`);
  // C is removed: ordering of remaining items unchanged.
  const fourth = applyStableOrder([d, b, a], third.order);
  assert(fourth.order.join(",") === "D,B,A", `removed drops out: ${fourth.order}`);
});

run("applyStableKeyOrder behaves the same on string keys", () => {
  let order = applyStableKeyOrder(["repo-c", "repo-b", "repo-a"], []);
  assert(order.join(",") === "repo-c,repo-b,repo-a", `initial: ${order}`);
  // repo-a bumps to top in the desired sort; stable order ignores that.
  order = applyStableKeyOrder(["repo-a", "repo-c", "repo-b"], order);
  assert(order.join(",") === "repo-c,repo-b,repo-a", `held: ${order}`);
  // new repo arrives.
  order = applyStableKeyOrder(["repo-d", "repo-c", "repo-b", "repo-a"], order);
  assert(order.join(",") === "repo-d,repo-c,repo-b,repo-a", `new: ${order}`);
});

console.log("\nconversationSort tests passed");
