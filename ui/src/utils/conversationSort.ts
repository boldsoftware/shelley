import type { Conversation, ConversationWithState } from "../types";

// Conversations are sorted into 5-minute "buckets" of their updated_at
// timestamp. Within a bucket, the most recently created comes first, so the
// order holds as they update. (Conversation ids are random, so they only
// break ties.) The bucketing avoids distracting flip-flops in the drawer when
// two conversations (or a conversation and its subagent) update within
// seconds of each other.
export const BUCKET_MS = 5 * 60 * 1000;

function time(field: string, timestamp: string): number {
  const t = new Date(timestamp).getTime();
  if (!Number.isFinite(t)) {
    throw new Error(`invalid ${field}: ${timestamp}`);
  }
  return t;
}

export function updatedBucket(updatedAt: string): number {
  return Math.floor(time("updated_at", updatedAt) / BUCKET_MS);
}

// Sort newest-bucket first, then newest-created first, then by
// conversation_id descending. Returns a new array; does not mutate the input.
export function sortConversationsByBucket<T extends Conversation>(convs: readonly T[]): T[] {
  return [...convs].sort((a, b) => {
    const ab = updatedBucket(a.updated_at);
    const bb = updatedBucket(b.updated_at);
    if (ab !== bb) return bb - ab;
    const created = time("created_at", b.created_at) - time("created_at", a.created_at);
    if (created !== 0) return created;
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

// applyStableOrder returns `sortedItems` reordered so that items present in
// `prevOrder` retain their relative position, and any items not seen before
// are prepended at the top in the order they appear in `sortedItems`. The
// returned id list should be stored and passed back as `prevOrder` on the
// next call. This lets the drawer keep its layout stable as conversations
// update, while still surfacing brand-new conversations at the top.
export function applyStableOrder<T extends { conversation_id: string }>(
  sortedItems: readonly T[],
  prevOrder: readonly string[],
): { items: T[]; order: string[] } {
  const byId = new Map<string, T>();
  for (const c of sortedItems) byId.set(c.conversation_id, c);
  const kept: string[] = [];
  const keptSet = new Set<string>();
  for (const id of prevOrder) {
    if (byId.has(id) && !keptSet.has(id)) {
      kept.push(id);
      keptSet.add(id);
    }
  }
  const newIds: string[] = [];
  for (const c of sortedItems) {
    if (!keptSet.has(c.conversation_id)) newIds.push(c.conversation_id);
  }
  const order = [...newIds, ...kept];
  return { items: order.map((id) => byId.get(id)!), order };
}

// applyStableKeyOrder is the string-key analogue of applyStableOrder, used
// to hold a stable order for group keys (cwd / git_repo).
export function applyStableKeyOrder(
  sortedKeys: readonly string[],
  prevOrder: readonly string[],
): string[] {
  const present = new Set(sortedKeys);
  const kept: string[] = [];
  const keptSet = new Set<string>();
  for (const k of prevOrder) {
    if (present.has(k) && !keptSet.has(k)) {
      kept.push(k);
      keptSet.add(k);
    }
  }
  const newKeys: string[] = [];
  for (const k of sortedKeys) {
    if (!keptSet.has(k)) newKeys.push(k);
  }
  return [...newKeys, ...kept];
}

// neighborAfterRemoval returns the conversation that should be selected after
// the conversation with `removedId` is removed from a visually-ordered list.
// The next selection is the conversation immediately below the removed one;
// if it was the last item, the one immediately above; null if there's no
// other conversation (or the id isn't present).
export function neighborAfterRemoval<T extends { conversation_id: string }>(
  order: readonly T[],
  removedId: string,
): T | null {
  const idx = order.findIndex((c) => c.conversation_id === removedId);
  if (idx < 0) return null;
  return order[idx + 1] ?? order[idx - 1] ?? null;
}
