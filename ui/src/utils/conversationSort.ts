import type { Conversation, ConversationWithState } from "../types";

// Conversations are sorted into 5-minute "buckets" of their updated_at
// timestamp. Within a bucket, ordering is stable by conversation_id, which
// is a ULID and therefore time-orderable by creation. The bucketing avoids
// distracting flip-flops in the drawer when two conversations (or a
// conversation and its subagent) update within seconds of each other.
export const BUCKET_MS = 5 * 60 * 1000;

export function updatedBucket(updatedAt: string): number {
  const t = new Date(updatedAt).getTime();
  if (!Number.isFinite(t)) {
    throw new Error(`invalid updated_at: ${updatedAt}`);
  }
  return Math.floor(t / BUCKET_MS);
}

// Sort newest-bucket first, then by conversation_id descending (newer ULID
// first). Returns a new array; does not mutate the input.
export function sortConversationsByBucket<T extends Conversation>(convs: readonly T[]): T[] {
  return [...convs].sort((a, b) => {
    const ab = updatedBucket(a.updated_at);
    const bb = updatedBucket(b.updated_at);
    if (ab !== bb) return bb - ab;
    if (a.conversation_id === b.conversation_id) return 0;
    return a.conversation_id < b.conversation_id ? 1 : -1;
  });
}

// Highest bucket value among the given conversations, used to order groups.
export function maxBucket(convs: readonly ConversationWithState[]): number {
  let best = -Infinity;
  for (const c of convs) {
    const b = updatedBucket(c.updated_at);
    if (b > best) best = b;
  }
  return best;
}
