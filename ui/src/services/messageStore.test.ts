// messageStore tests — IndexedDB-backed per-message cache.
//
// Each test gets a fresh MessageStore over a fresh IDBFactory, so cases are
// fully isolated and we can simulate "another tab" by opening a second store
// over the same factory + dbName.
//
// Run via `pnpm test` (see scripts/run-tests.mjs).

// Use the auto polyfill so IDBKeyRange, IDBRequest, etc. are present as
// globals, then construct fresh per-test IDBFactory instances for isolation.
import "fake-indexeddb/auto";
import { IDBFactory } from "fake-indexeddb";
import { webcrypto } from "node:crypto";
import { MessageStore } from "./messageStore";
import type { ConversationCacheRecord } from "./messageStore";
import type { Conversation, Message, StreamResponse } from "../types";
import { CacheKeyHolder, type CacheKeyFetcher, type CacheKeyMaterial } from "./cryptoKey";
import { cacheDiagStats } from "./cacheDiag";

// Node 20 lacks a global `crypto.subtle`; expose webcrypto so messageStore's
// AES-GCM helpers work in tests.
if (typeof globalThis.crypto === "undefined" || !globalThis.crypto.subtle) {
  Object.defineProperty(globalThis, "crypto", { value: webcrypto, configurable: true });
}

/** Static fetcher that returns the same key every call. */
class StaticFetcher implements CacheKeyFetcher {
  private cleared = false;
  constructor(
    private keyId: string,
    private rawKey: Uint8Array,
  ) {}
  async fetch(): Promise<CacheKeyMaterial> {
    const buf = new ArrayBuffer(this.rawKey.byteLength);
    new Uint8Array(buf).set(this.rawKey);
    const key = await crypto.subtle.importKey("raw", buf, { name: "AES-GCM" }, false, [
      "encrypt",
      "decrypt",
    ]);
    return { keyId: this.keyId, key, alg: "AES-GCM-256" };
  }
  async clear(): Promise<void> {
    this.cleared = true;
  }
  wasCleared(): boolean {
    return this.cleared;
  }
  rotate(keyId: string, rawKey: Uint8Array): void {
    this.keyId = keyId;
    this.rawKey = rawKey;
  }
}

function randomKey(): Uint8Array {
  const k = new Uint8Array(32);
  crypto.getRandomValues(k);
  return k;
}

/**
 * The catch-up predicate as evaluated by ChatInterface.loadMessages: the UI
 * must talk to the server when the cache lacks full history, when the
 * server-reported max sequence_id is ahead of what we have locally, or when a
 * stream reconnect flagged the record for re-verification.
 *
 * Mirrors ChatInterface's `cacheIsComplete && !needsRefresh` early return.
 * Keep the two in sync — the tests below encode the safety properties the
 * store guarantees, and they only hold if the UI reads the record this way.
 */
function needsBackfill(rec: ConversationCacheRecord | null): boolean {
  if (!rec || !rec.hasFullHistory) return true;
  if (rec.needsRefresh) return true;
  if (rec.maxSequenceIdKnown <= 0) return false;
  return rec.maxSequenceId < rec.maxSequenceIdKnown;
}

/**
 * Whether that server round-trip has to be a FULL re-download rather than a
 * cheap `?last_sequence_id=` tail fetch. Mirrors ChatInterface's guard on the
 * incremental branch (`cached.hasFullHistory && cached.messages.length > 0`).
 *
 * This is why hasFullHistory is the flag the gap checks clear, and
 * needsRefresh is not: a tail fetch starts from our cached max and so can
 * never recover a message missing from the middle.
 */
function needsFullReload(rec: ConversationCacheRecord | null): boolean {
  return !rec || !rec.hasFullHistory || rec.messages.length === 0;
}

/**
 * Deadlines used by the multi-tab liveness tests. Short enough to keep the
 * suite fast, long enough not to race the event loop.
 */
const SHORT_TIMEOUT_MS = 80;
/** Must match IDB_OPEN_COOLDOWN_MS's role: how long a timeout suppresses retries. */
const SHORT_COOLDOWN_MS = 80;
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

let seq = 0;
/**
 * Per-test factory + dbName + cache key so each case is fully isolated.
 * The key is RANDOM per test — different tests cannot read each other's
 * IDB rows even though they share the IDBFactory process.
 */
function freshFactory(): {
  factory: IDBFactory;
  dbName: string;
  keyId: string;
  rawKey: Uint8Array;
} {
  return {
    factory: new IDBFactory(),
    dbName: `shelley-messages-test-${++seq}`,
    keyId: `kid-${seq}`,
    rawKey: randomKey(),
  };
}
function storeFor(fixture: {
  factory: IDBFactory;
  dbName: string;
  keyId: string;
  rawKey: Uint8Array;
  openTimeoutMs?: number;
  openCooldownMs?: number;
  txTimeoutMs?: number;
}): MessageStore {
  const fetcher = new StaticFetcher(fixture.keyId, fixture.rawKey);
  return new MessageStore({
    factory: fixture.factory,
    dbName: fixture.dbName,
    keyHolder: new CacheKeyHolder(fetcher),
    openTimeoutMs: fixture.openTimeoutMs,
    openCooldownMs: fixture.openCooldownMs,
    txTimeoutMs: fixture.txTimeoutMs,
  });
}
function freshStore(): MessageStore {
  return storeFor(freshFactory());
}

function conv(convId: string, agentWorking: boolean): Conversation {
  return {
    conversation_id: convId,
    slug: convId,
    user_initiated: true,
    created_at: new Date(0).toISOString(),
    updated_at: new Date(0).toISOString(),
    cwd: null,
    archived: false,
    parent_conversation_id: null,
    model: null,
    conversation_options: "{}",
    current_generation: 0,
    agent_working: agentWorking,
    tags: "[]",
    is_draft: false,
    draft: "",
    queued_messages: "[]",
  };
}

function msg(convId: string, sequence_id: number, msgId?: string): Message {
  return {
    message_id: msgId ?? `${convId}-${sequence_id}`,
    conversation_id: convId,
    sequence_id,
    type: "user",
    llm_data: null,
    user_data: null,
    usage_data: null,
    created_at: new Date(sequence_id * 1000).toISOString(),
    display_data: null,
    generation: 0,
    end_of_turn: false,
  };
}

function assert(cond: boolean, message: string): void {
  if (!cond) throw new Error(`Assertion failed: ${message}`);
}

/**
 * Cap every case so a regression that reintroduces an unbounded wait FAILS
 * rather than hanging. This file exists to pin liveness properties, and a
 * suite that hangs reports nothing at all — the same "no evidence" failure
 * mode as the bug being tested.
 */
const CASE_TIMEOUT_MS = 20_000;

async function run(name: string, fn: () => Promise<void>): Promise<void> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    await Promise.race([
      fn(),
      new Promise<never>((_res, rej) => {
        timer = setTimeout(
          () => rej(new Error(`case exceeded ${CASE_TIMEOUT_MS}ms (likely an unbounded wait)`)),
          CASE_TIMEOUT_MS,
        );
      }),
    ]);
    console.log(`✓ ${name}`);
  } catch (err) {
    console.error(`✗ ${name}`);
    throw err;
  } finally {
    clearTimeout(timer);
  }
}

async function main(): Promise<void> {
  await run("upsertMessages + hydrate round-trip in seq order", async () => {
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c1";
    s.upsertMessages(id, [msg(id, 3), msg(id, 1), msg(id, 2)]);
    await s.settle();

    const rec = s.peek(id)!;
    assert(rec.messages.length === 3, `peek len ${rec.messages.length}`);
    assert(
      rec.messages[0].sequence_id === 1 &&
        rec.messages[1].sequence_id === 2 &&
        rec.messages[2].sequence_id === 3,
      "in-memory sorted",
    );
    assert(rec.minSequenceId === 1 && rec.maxSequenceId === 3, "min/max");

    // Cross-instance: open a fresh store on the same db and hydrate.
    await s.close();
    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = await s2.hydrate(id);
    assert(hyd !== null, "hydrate non-null");
    assert(hyd!.messages.length === 3, `hydrated len ${hyd!.messages.length}`);
    assert(
      hyd!.messages[0].sequence_id === 1 && hyd!.messages[2].sequence_id === 3,
      "hydrated sorted asc",
    );
  });

  await run("monotonic head: older seq does not lower max_sequence_id_local", async () => {
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c1";
    s.upsertMessages(id, [msg(id, 5)]);
    await s.settle();
    s.upsertMessages(id, [msg(id, 3)]);
    await s.settle();
    assert(s.peek(id)!.maxSequenceId === 5, "in-memory max stays 5");

    // Cross-instance verifies the persisted meta row.
    await s.close();
    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = await s2.hydrate(id);
    assert(hyd!.maxSequenceId === 5, `persisted max ${hyd!.maxSequenceId} != 5`);
  });

  await run("idempotent re-upsert: same [conv, seq] twice = one row", async () => {
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c1";
    const m = msg(id, 1, "only");
    s.upsertMessages(id, [m]);
    s.upsertMessages(id, [m]);
    await s.settle();
    assert(s.peek(id)!.messages.length === 1, "in-memory one row");
    await s.close();
    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = await s2.hydrate(id);
    assert(hyd!.messages.length === 1, `persisted ${hyd!.messages.length} != 1`);
  });

  await run("concurrent appends preserve max across store instances", async () => {
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const id = "c1";
    const sA = storeFor({ factory, dbName, keyId, rawKey });
    const sB = storeFor({ factory, dbName, keyId, rawKey });
    sA.upsertMessages(id, [msg(id, 0, "m0")]);
    await sA.settle();
    sA.upsertMessages(id, [msg(id, 10, "ma")]);
    sB.upsertMessages(id, [msg(id, 7, "mb")]);
    sA.upsertMessages(id, [msg(id, 12, "mc")]);
    await Promise.all([sA.settle(), sB.settle()]);
    await sA.close();
    await sB.close();

    const s3 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = await s3.hydrate(id);
    assert(hyd !== null, "hydrate non-null");
    assert(hyd!.maxSequenceId === 12, `expected max=12, got ${hyd!.maxSequenceId}`);
    assert(hyd!.messages.length === 4, `expected 4 rows, got ${hyd!.messages.length}`);
  });

  await run("delete() removes rows + meta (cross-instance verified)", async () => {
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-del";
    s.upsertMessages(id, [msg(id, 1), msg(id, 2)]);
    s.upsertMessages("c-keep", [msg("c-keep", 1)]);
    await s.settle();
    await s.delete(id);
    await s.close();

    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    const gone = await s2.hydrate(id);
    assert(gone === null, "deleted conv gone after fresh hydrate");
    const kept = await s2.hydrate("c-keep");
    assert(kept !== null && kept.messages.length === 1, "other conv preserved");
  });

  // ── Poisoned-hydration regressions ────────────────────────────────────────
  //
  // Bookkeeping/metadata mutators used to mark a conversation `hydrated`
  // even though nothing had been read from IDB. On a page load App calls
  // setMaxSequenceIdKnown() for EVERY conversation in the list before any
  // of them is focused, so the disk cache was never read and every focus
  // did a full REST reload — the cache only ever worked for in-session
  // conversation switches.

  await run("setMaxSequenceIdKnown before hydrate must not poison the cache", async () => {
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const id = "c-poison-known";
    const s = storeFor({ factory, dbName, keyId, rawKey });
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [msg(id, 1), msg(id, 2), msg(id, 3)],
      context_window_size: 42,
      max_sequence_id: 3,
    });
    await s.settle();
    await s.close();

    // Fresh page load: the conversation-list snapshot lands first.
    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    s2.setMaxSequenceIdKnown(id, 3);
    assert(!s2.isHydrated(id), "list metadata must not count as hydration");
    const hyd = await s2.hydrate(id);
    assert(hyd !== null, "hydrate must read the disk cache");
    assert(hyd!.messages.length === 3, `expected 3 cached messages, got ${hyd!.messages.length}`);
    assert(hyd!.hasFullHistory, "hasFullHistory preserved through metadata patch");
    assert(hyd!.maxSequenceIdKnown === 3, `known=${hyd!.maxSequenceIdKnown}`);
    assert(hyd!.contextWindowSize === 42, `ctx=${hyd!.contextWindowSize}`);
    assert(!needsBackfill(hyd), "a fully-cached conversation must not need a REST reload");
    await s2.close();
  });

  await run("setConversation before hydrate must not poison the cache", async () => {
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const id = "c-poison-conv";
    const s = storeFor({ factory, dbName, keyId, rawKey });
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [msg(id, 1), msg(id, 2)],
      context_window_size: 7,
      max_sequence_id: 2,
    });
    await s.settle();
    await s.close();

    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    s2.setConversation(id, conv(id, false));
    s2.setContextWindowSize(id, 9);
    const hyd = await s2.hydrate(id);
    assert(hyd !== null, "hydrate must read the disk cache");
    assert(hyd!.messages.length === 2, `expected 2 cached messages, got ${hyd!.messages.length}`);
    assert(hyd!.hasFullHistory, "hasFullHistory preserved");
    assert(
      hyd!.contextWindowSize === 9,
      `fresher in-memory ctx wins, got ${hyd!.contextWindowSize}`,
    );
    await s2.close();
  });

  await run("hydrate merges live messages that landed before hydration", async () => {
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const id = "c-live-first";
    const s = storeFor({ factory, dbName, keyId, rawKey });
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [msg(id, 1), msg(id, 2)],
      context_window_size: 0,
      max_sequence_id: 2,
    });
    await s.settle();
    await s.close();

    // New tab: a live stream event for this conversation arrives before the
    // user focuses it, then hydration runs. Neither may drop the other's data.
    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    s2.upsertMessages(id, [msg(id, 3)]);
    const hyd = await s2.hydrate(id);
    assert(hyd !== null, "hydrated");
    assert(hyd!.messages.length === 3, `expected merged 1..3, got ${hyd!.messages.length}`);
    assert(hyd!.maxSequenceId === 3, `max=${hyd!.maxSequenceId}`);
    assert(hyd!.hasFullHistory, "disk full-history flag survives the merge");
    assert(!needsBackfill(hyd), "merged cache is complete");
    assert(s2.peek(id)!.messages.length === 3, "hot record updated in place");
    await s2.close();
  });

  await run("hydrate is retried after a cache-key outage", async () => {
    // If the server refuses to release the cache key (auth blip), we must
    // NOT record the conversation as hydrated: a later hydrate() has to be
    // able to pick up the disk cache once the key is available again.
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const id = "c-key-outage";
    const s = storeFor({ factory, dbName, keyId, rawKey });
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [msg(id, 1)],
      context_window_size: 0,
      max_sequence_id: 1,
    });
    await s.settle();
    await s.close();

    class FlakyFetcher implements CacheKeyFetcher {
      fail = true;
      constructor(
        private keyId: string,
        private rawKey: Uint8Array,
      ) {}
      async fetch(): Promise<CacheKeyMaterial> {
        if (this.fail) throw new Error("simulated 503");
        const buf = new ArrayBuffer(this.rawKey.byteLength);
        new Uint8Array(buf).set(this.rawKey);
        const key = await crypto.subtle.importKey("raw", buf, { name: "AES-GCM" }, false, [
          "encrypt",
          "decrypt",
        ]);
        return { keyId: this.keyId, key, alg: "AES-GCM-256" };
      }
      async clear(): Promise<void> {}
    }
    const fetcher = new FlakyFetcher(keyId, rawKey);
    const s2 = new MessageStore({ factory, dbName, keyHolder: new CacheKeyHolder(fetcher) });
    assert((await s2.hydrate(id)) === null, "no key => no cache");
    assert(!s2.isHydrated(id), "key outage must stay retryable");
    fetcher.fail = false;
    const hyd = await s2.hydrate(id);
    assert(hyd !== null && hyd.messages.length === 1, "cache readable once the key returns");
    await s2.close();
  });

  await run(
    "applyFullHistory replace+dedup+sort within snapshot range (cross-instance)",
    async () => {
      const { factory, dbName, keyId, rawKey } = freshFactory();
      const s = storeFor({ factory, dbName, keyId, rawKey });
      const id = "c-full";
      // A local row WITHIN the snapshot's range (seq 2) is authoritative-replaced
      // by the snapshot (same seq, different message_id), not duplicated.
      s.upsertMessages(id, [msg(id, 2, "stale-dupe")]);
      await s.settle();
      const resp: StreamResponse = {
        conversation_id: id,
        messages: [msg(id, 2), msg(id, 1), msg(id, 3)],
        context_window_size: 100,
        max_sequence_id: 3,
      };
      s.applyFullHistory(id, resp);
      await s.settle();
      await s.close();

      const s2 = storeFor({ factory, dbName, keyId, rawKey });
      const hyd = await s2.hydrate(id);
      assert(hyd !== null, "hydrated");
      assert(hyd!.messages.length === 3, `expected 3, got ${hyd!.messages.length}`);
      assert(
        hyd!.messages[0].sequence_id === 1 &&
          hyd!.messages[1].sequence_id === 2 &&
          hyd!.messages[2].sequence_id === 3,
        "sorted asc",
      );
      assert(
        hyd!.messages.every((m) => m.message_id !== "stale-dupe"),
        "in-range row replaced by snapshot",
      );
      assert(hyd!.hasFullHistory === true, "hasFullHistory persisted");
    },
  );

  await run("applyFullHistory does not regress messages newer than the snapshot", async () => {
    // Regression guard: a REST backfill can be STALE relative to live data
    // (the fetch was issued before an agent turn committed but resolved only
    // after the live stream delivered the newer messages). applyFullHistory
    // must keep locally-cached messages beyond the snapshot's tail rather than
    // clobbering them — otherwise a brand-new conversation gets stuck showing
    // only the first message until reload.
    const s = freshStore();
    const id = "c-race";
    // Live stream already delivered the full conversation (seqs 1..3).
    s.upsertMessages(id, [msg(id, 1), msg(id, 2), msg(id, 3)]);
    // A stale REST snapshot taken earlier only has seq 1.
    const stale: StreamResponse = {
      conversation_id: id,
      messages: [msg(id, 1)],
      context_window_size: 100,
      max_sequence_id: 1,
    };
    s.applyFullHistory(id, stale);
    const rec = s.peek(id);
    assert(rec !== null, "record present");
    assert(rec!.messages.length === 3, `expected 3 (no regression), got ${rec!.messages.length}`);
    assert(rec!.maxSequenceId === 3, `expected maxSequenceId 3, got ${rec!.maxSequenceId}`);
  });

  await run("backfill detection via maxSequenceIdKnown", async () => {
    const s = freshStore();
    const id = "c-back";
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [msg(id, 1), msg(id, 2), msg(id, 3)],
      context_window_size: 0,
    });
    let rec = s.peek(id)!;
    assert(rec.hasFullHistory, "has full hist");
    s.setMaxSequenceIdKnown(id, 3);
    rec = s.peek(id)!;
    assert(rec.maxSequenceIdKnown <= rec.maxSequenceId, "up-to-date when known==local");
    s.setMaxSequenceIdKnown(id, 5);
    rec = s.peek(id)!;
    assert(rec.maxSequenceIdKnown > rec.maxSequenceId, "stale when known>local");
    await s.settle();
  });

  await run("markAllStale flags a refresh without discarding the cache", async () => {
    // After a stream reconnect we must re-check with the server, but the
    // cached history is still complete and renderable: keep hasFullHistory
    // so the UI can paint from cache and fetch only the tail.
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-stale";
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [msg(id, 1), msg(id, 2), msg(id, 3)],
      context_window_size: 0,
    });
    await s.settle();
    s.markAllStale();
    await s.settle();
    assert(s.peek(id)!.needsRefresh === true, "needsRefresh set");
    assert(s.peek(id)!.hasFullHistory === true, "cached history still usable");
    assert(!needsFullReload(s.peek(id)), "tail fetch suffices");
    assert(s.peek(id)!.messages.length === 3, "hot messages preserved");
    await s.close();

    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = await s2.hydrate(id);
    assert(hyd !== null && hyd.messages.length === 3, "messages preserved on disk");
    assert(hyd!.needsRefresh === true, "needs_refresh persisted");
    assert(hyd!.hasFullHistory === true, "has_full_history persisted");
    // And clearing it (after a successful tail fetch) sticks.
    s2.clearNeedsRefresh(id);
    await s2.settle();
    await s2.close();
    const s3 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd3 = await s3.hydrate(id);
    assert(hyd3!.needsRefresh === false, "needs_refresh cleared on disk");
    await s3.close();
  });

  await run("applyIncrementalTail appends only the tail and clears needsRefresh", async () => {
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-tail";
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [msg(id, 1), msg(id, 2)],
      context_window_size: 0,
      max_sequence_id: 2,
    });
    await s.settle();
    s.markAllStale();
    s.setMaxSequenceIdKnown(id, 4);
    await s.settle();
    // The server answers ?last_sequence_id=2 with just 3 and 4.
    s.applyIncrementalTail(
      id,
      {
        conversation_id: id,
        messages: [msg(id, 3), msg(id, 4)],
        context_window_size: 0,
      },
      2,
    );
    await s.settle();
    const rec = s.peek(id)!;
    assert(rec.messages.length === 4, `expected 4 messages, got ${rec.messages.length}`);
    assert(rec.maxSequenceId === 4, `max=${rec.maxSequenceId}`);
    assert(rec.hasFullHistory, "still full history");
    assert(rec.needsRefresh === false, "needsRefresh cleared");
    assert(!needsBackfill(rec), "caught up");
    await s.close();
    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = await s2.hydrate(id);
    assert(hyd!.messages.length === 4, `persisted ${hyd!.messages.length} messages`);
    assert(hyd!.hasFullHistory, "has_full_history persisted");
    assert(hyd!.needsRefresh === false, "needs_refresh persisted false");
    await s2.close();
  });

  await run("a server snapshot with sequence gaps stays fully cached", async () => {
    // Sequence ids are NOT guaranteed consecutive. db.CopyMessagesForFork
    // copies only the fork point's generation and PRESERVES the source
    // sequence_ids, so forking a conversation whose sequence space has
    // messages from an abandoned generation in the middle (e.g. after a
    // rolled-back compaction — see server/distill_pi.go
    // rollbackCompactionFailure) yields a complete history with real holes.
    // The REST snapshot is authoritative: a hole in it must not be mistaken
    // for cache damage, or such conversations would re-download forever.
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-forked-gap";
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [msg(id, 1), msg(id, 2), msg(id, 7), msg(id, 8)],
      context_window_size: 0,
      max_sequence_id: 8,
    });
    await s.settle();
    assert(!needsBackfill(s.peek(id)), "complete in memory");
    await s.close();

    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = await s2.hydrate(id);
    assert(hyd !== null, "hydrated");
    assert(hyd!.messages.length === 4, `got ${hyd!.messages.length}`);
    assert(hyd!.hasFullHistory, "a gappy but authoritative snapshot is still full");
    assert(!needsBackfill(hyd), "must serve from cache, not reload forever");
    await s2.close();
  });

  await run("an empty draft cache rejects a streamed sequence-2 first message", async () => {
    // Draft creation seeds an empty cache as complete so focusing the draft
    // does not blur the composer behind a loading state. Promotion creates the
    // system prompt as sequence 1 without broadcasting it, then streams the
    // user's message as sequence 2. Treating that append as contiguous poisons
    // the cache permanently: the system-prompt card disappears in this browser
    // while a cold browser fetches the complete server history and shows it.
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-draft-prefix-race";
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [],
      context_window_size: 0,
      max_sequence_id: 0,
    });
    await s.settle();

    s.upsertMessages(id, [msg(id, 2)]);
    const rec = s.peek(id)!;
    assert(rec.hasFullHistory === false, "sequence 2 cannot extend an empty full history");
    assert(needsBackfill(rec), "the missing system prompt must force a full REST repair");
    await s.settle();
    await s.close();

    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = await s2.hydrate(id);
    assert(hyd !== null, "hydrated");
    assert(hyd!.hasFullHistory === false, "the missing prefix stays invalid on disk");
    await s2.close();
  });

  await run("an empty snapshot cannot certify grafted live rows as full history", async () => {
    // The draft-promotion race, from the REST side. The server flips is_draft
    // and announces it BEFORE creating the system prompt as sequence 1, and
    // that row is never broadcast. A refetch landing in that window returns
    // zero messages while the streamed user message (sequence 2) is already
    // cached, so applyFullHistory merges [2] and would historically stamp it
    // complete -- permanently hiding the system prompt in this browser.
    // upsertMessages' join check cannot save us here: seq 2 arrived BEFORE the
    // certification, so there was nothing for it to compare against.
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-promotion-empty-snapshot";

    // Live stream delivers the user's message first.
    s.upsertMessages(id, [msg(id, 2)]);
    // The racing refetch comes back empty.
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [],
      context_window_size: 0,
      max_sequence_id: 0,
    });

    const rec = s.peek(id)!;
    assert(rec.messages.length === 1, "the live row is preserved, not dropped");
    assert(rec.messages[0].sequence_id === 2, "the preserved row is sequence 2");
    assert(
      rec.hasFullHistory === false,
      "an empty snapshot cannot certify a history starting at 2",
    );
    assert(needsBackfill(rec), "the next focus must fetch the full history");
    await s.settle();
    await s.close();

    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = await s2.hydrate(id);
    assert(hyd !== null, "hydrated");
    assert(hyd!.hasFullHistory === false, "the refusal must survive a reload");
    await s2.close();
  });

  await run("an empty snapshot still certifies when the graft starts at sequence 1", async () => {
    // Same race, but the stream delivered sequence 1 before the refetch
    // resolved. The merged set genuinely starts at the beginning, so it is a
    // complete history and must stay certified -- this is the case
    // applyFullHistory's newerLocal merge exists to protect.
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-empty-snapshot-from-one";

    s.upsertMessages(id, [msg(id, 1), msg(id, 2)]);
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [],
      context_window_size: 0,
      max_sequence_id: 0,
    });

    const rec = s.peek(id)!;
    assert(rec.hasFullHistory === true, "a graft starting at sequence 1 is a complete history");
    assert(!needsBackfill(rec), "no repair fetch is needed");
    await s.settle();
    await s.close();
  });

  await run("a fork whose history starts past sequence 1 stays certified", async () => {
    // db.CopyMessagesForFork copies a single generation while PRESERVING the
    // source sequence ids, so a fork taken after a compaction legitimately
    // begins well past 1. A full GET returns that whole history, so whatever
    // it starts at IS the start. Treating "first row > 1" as damage would make
    // these conversations re-download everything on every focus, forever.
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-fork-offset-history";
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [msg(id, 20), msg(id, 21)],
      context_window_size: 0,
      max_sequence_id: 21,
    });
    const rec = s.peek(id)!;
    assert(rec.hasFullHistory === true, "a full GET starting at 20 is still complete");
    await s.settle();
    await s.close();

    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = await s2.hydrate(id);
    assert(hyd !== null, "hydrated");
    assert(hyd!.hasFullHistory === true, "a fork's offset history survives a reload certified");
    assert(!needsBackfill(hyd), "a fork must not refetch its whole history on every focus");
    await s2.close();
  });

  await run("a redelivered message replaces the cached copy, not duplicates it", async () => {
    // Redelivery is normal: a stream reconnect can resend rows we already
    // hold, and subpub replays from a cursor. Upserting by message_id must be
    // idempotent — same count, newest copy wins — and must not look like a gap.
    //
    // (Messages are append-only in the database; nothing rewrites a row's
    // content in place. Data that arrives later, such as the cost of the LLM
    // call that named the conversation, is recorded by appending a new row.)
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-mutated";
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [msg(id, 1), msg(id, 2)],
      context_window_size: 0,
      max_sequence_id: 2,
    });
    await s.settle();

    const updated: Message = { ...msg(id, 1), other_usage_data: '[{"purpose":"slug"}]' };
    s.upsertMessages(id, [updated]);
    await s.settle();
    const rec = s.peek(id)!;
    assert(rec.messages.length === 2, `re-publish must not duplicate, got ${rec.messages.length}`);
    assert(rec.messages[0].other_usage_data !== null, "in-memory copy updated");
    assert(rec.hasFullHistory, "redelivery of a known message is not a gap");
    await s.close();

    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = await s2.hydrate(id);
    assert(hyd!.messages.length === 2, `persisted ${hyd!.messages.length} rows, want 2`);
    assert(
      hyd!.messages[0].other_usage_data === updated.other_usage_data,
      "persisted copy updated",
    );
    assert(hyd!.hasFullHistory, "hasFullHistory survives a redelivery");
    await s2.close();
  });

  await run("a hole opened by a live upsert forces a full reload, not a tail fetch", async () => {
    // Full history for 1..3, then a live event delivers 7 (4..6 happened while
    // we weren't listening). The store must not report "caught up" just
    // because local max (7) now equals known max (7) — the 4..6 hole has to be
    // repaired, and repaired with a FULL reload, since a ?last_sequence_id=7
    // tail fetch would never return the missing middle.
    const s = freshStore();
    const id = "c-hole-then-caught-up";
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [msg(id, 1), msg(id, 2), msg(id, 3)],
      context_window_size: 0,
      max_sequence_id: 3,
    });
    s.setMaxSequenceIdKnown(id, 7);
    s.upsertMessages(id, [msg(id, 7)]);
    const rec = s.peek(id)!;
    assert(rec.maxSequenceId === 7, `local=${rec.maxSequenceId}`);
    assert(rec.maxSequenceIdKnown === 7, `known=${rec.maxSequenceIdKnown}`);
    assert(rec.hasFullHistory === false, "the hole must clear hasFullHistory");
    assert(needsBackfill(rec), "sequence counters agreeing must not mask the hole");
    assert(needsFullReload(rec), "a tail fetch cannot recover a missing middle");
    await s.settle();
    // The hole must also be recorded on disk, or the next page load would
    // hydrate a record that claims to be complete.
    const hyd = await s.hydrate(id);
    assert(hyd!.hasFullHistory === false, "hole persisted");
    await s.close();
  });

  await run("a pre-hydration live append cannot launder an incomplete cache", async () => {
    // The dangerous ordering: a live message for a not-yet-focused
    // conversation is PERSISTED (settled) before hydration runs, so hydrate
    // reads the merged set off disk and mergeRecords never sees a hot-only
    // message to check. _persistUpsert must therefore do the join check
    // itself and clear has_full_history, or the hole becomes invisible.
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-launder";
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [msg(id, 1), msg(id, 2), msg(id, 3)],
      context_window_size: 0,
      max_sequence_id: 3,
    });
    await s.settle();
    await s.close();

    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    s2.upsertMessages(id, [msg(id, 7)]);
    await s2.settle(); // persisted BEFORE hydrate — the production ordering
    await s2.close();

    const s3 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = await s3.hydrate(id);
    assert(hyd !== null, "hydrated");
    assert(hyd!.messages.length === 4, `got ${hyd!.messages.length}`);
    assert(hyd!.hasFullHistory === false, "a skipped range must not survive persistence");
    assert(needsBackfill(hyd), "must repair");
    await s3.close();
  });

  await run("a lost row plus a later append still forfeits hasFullHistory", async () => {
    // message_count must be RATCHETED, not reset to the observed row count:
    // losing one row and then appending one would otherwise restore the
    // count and re-certify a cache that is missing a message.
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-launder2";
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [msg(id, 1), msg(id, 2), msg(id, 3)],
      context_window_size: 0,
      max_sequence_id: 3,
    });
    await s.settle();
    await s.close();

    // Lose the middle row behind the store's back.
    const req = factory.open(dbName, 4);
    const raw: IDBDatabase = await new Promise((resolve, reject) => {
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
    await new Promise<void>((resolve, reject) => {
      const r = raw.transaction("messages", "readwrite").objectStore("messages").delete([id, 2]);
      r.onsuccess = () => resolve();
      r.onerror = () => reject(r.error);
    });
    raw.close();

    // Now a legitimate live append restores the row count to 3.
    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    s2.upsertMessages(id, [msg(id, 4)]);
    await s2.settle();
    await s2.close();

    const s3 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = await s3.hydrate(id);
    assert(hyd !== null, "hydrated");
    assert(hyd!.messages.length === 3, `got ${hyd!.messages.length}`);
    assert(hyd!.hasFullHistory === false, "count must not be launderable");
    assert(needsBackfill(hyd), "must repair");
    await s3.close();
  });

  await run("markAllStale during an in-flight hydrate is not lost", async () => {
    // A reconnect landing mid-hydrate applies to the record about to be
    // installed: the disk row predates the reconnect, so its needs_refresh
    // cannot reflect it. Losing the flag would leave the conversation
    // serving stale cache with no server re-check.
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-stale-race";
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [msg(id, 1), msg(id, 2)],
      context_window_size: 0,
      max_sequence_id: 2,
    });
    await s.settle();
    await s.close();

    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    const pending = s2.hydrate(id);
    // Fire the reconnect while the read is in flight. The hot map is still
    // empty here, so the loop over hot records has nothing to flag.
    s2.markAllStale();
    const hyd = await pending;
    assert(hyd !== null, "hydrated");
    assert(hyd!.needsRefresh === true, "reconnect flag survived the hydrate");
    assert(needsBackfill(hyd), "still re-checks with the server");
    await s2.settle();
    await s2.close();
    // And it was persisted, so a later tab also re-checks.
    const s3 = storeFor({ factory, dbName, keyId, rawKey });
    assert((await s3.hydrate(id))!.needsRefresh === true, "needs_refresh persisted");
    await s3.close();
  });

  await run("applyIncrementalTail on a vanished record does not claim full history", async () => {
    // The record can be deleted (archive / prune / cache wipe) while the
    // tail fetch is in flight. The response covers only sequence_ids > N, so
    // treating it as a full snapshot would cache a headless conversation.
    const s = freshStore();
    const id = "c-vanished";
    s.applyIncrementalTail(
      id,
      { conversation_id: id, messages: [msg(id, 9)], context_window_size: 0 },
      8,
    );
    await s.settle();
    const rec = s.peek(id);
    assert(rec !== null, "tail still cached for the live view");
    assert(rec!.hasFullHistory === false, "a tail is not a full history");
    assert(needsBackfill(rec), "next focus must do a real backfill");
    await s.close();
  });

  await run("lost message rows forfeit hasFullHistory", async () => {
    // Rows can go missing (undecryptable, partial wipe, prune race). A short
    // read means the cached history has a hole, so it must be repaired
    // rather than rendered as if it were complete. Detected by row COUNT
    // (message_count), not sequence contiguity — see the fork case above.
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-gap";
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [msg(id, 1), msg(id, 2), msg(id, 3)],
      context_window_size: 0,
      max_sequence_id: 3,
    });
    await s.settle();
    await s.close();

    // Delete the middle row behind the store's back.
    const req = factory.open(dbName, 4);
    const raw: IDBDatabase = await new Promise((resolve, reject) => {
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
    await new Promise<void>((resolve, reject) => {
      const r = raw.transaction("messages", "readwrite").objectStore("messages").delete([id, 2]);
      r.onsuccess = () => resolve();
      r.onerror = () => reject(r.error);
    });
    raw.close();

    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = await s2.hydrate(id);
    assert(hyd !== null, "hydrated");
    assert(hyd!.messages.length === 2, `got ${hyd!.messages.length}`);
    assert(hyd!.hasFullHistory === false, "lost rows must clear hasFullHistory");
    assert(needsBackfill(hyd), "lost rows force a REST backfill");
    await s2.close();
  });

  await run("live messages that skip past the cached tail are not trusted", async () => {
    // Previous session cached 1..3; messages 4..6 were added while no tab
    // was open; now a live stream event delivers 7 before the conversation
    // is focused. Merging blindly would leave a hole in the middle while
    // every counter says we're caught up, so the user would silently see a
    // conversation missing messages.
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-skip";
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [msg(id, 1), msg(id, 2), msg(id, 3)],
      context_window_size: 0,
      max_sequence_id: 3,
    });
    await s.settle();
    await s.close();

    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    s2.upsertMessages(id, [msg(id, 7)]);
    const hyd = await s2.hydrate(id);
    assert(hyd !== null, "hydrated");
    assert(hyd!.messages.length === 4, "both sets kept for a fast paint");
    assert(hyd!.hasFullHistory === false, "a skipped range must clear hasFullHistory");
    assert(needsBackfill(hyd), "a skipped range forces a repair");
    await s2.close();
  });

  await run("setConversation does not disturb max_sequence_id_local", async () => {
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-conv";
    s.upsertMessages(id, [msg(id, 5)]);
    await s.settle();
    s.setConversation(id, {
      conversation_id: id,
      slug: "hello",
      title: "hello",
    } as unknown as import("../types").Conversation);
    await s.settle();
    await s.close();
    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = await s2.hydrate(id);
    assert(hyd!.maxSequenceId === 5, `max preserved, got ${hyd!.maxSequenceId}`);
    assert(hyd!.conversation !== null, "conversation persisted");
  });

  await run("setContextWindowSize does not disturb max_sequence_id_local", async () => {
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-ctx";
    s.upsertMessages(id, [msg(id, 7)]);
    await s.settle();
    s.setContextWindowSize(id, 4321);
    await s.settle();
    await s.close();
    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = await s2.hydrate(id);
    assert(hyd!.maxSequenceId === 7, `max preserved, got ${hyd!.maxSequenceId}`);
    assert(hyd!.contextWindowSize === 4321, "ctx persisted");
  });

  // globalStream.handleEvent calls upsertMessages() and then
  // setContextWindowSize() for the same stream frame, without awaiting in
  // between. Both persist the encrypted meta payload with a read-modify-write
  // whose snapshot is taken before the write, so the slower writer
  // (_persistUpsert, which also encrypts every message row) must not stomp the
  // context size the faster one committed meanwhile — otherwise the next page
  // load hydrates contextWindowSize 0 and the status bar reads "0".
  await run("upsertMessages then setContextWindowSize in one tick persists ctx", async () => {
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-ctx-race";
    s.upsertMessages(id, [msg(id, 1), msg(id, 2)]);
    s.setContextWindowSize(id, 14921);
    await s.settle();
    await s.close();
    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = await s2.hydrate(id);
    assert(hyd!.contextWindowSize === 14921, `ctx persisted, got ${hyd!.contextWindowSize}`);
  });

  await run(
    "applyFullHistory ratchets maxSequenceIdKnown against response.max_sequence_id",
    async () => {
      const s = freshStore();
      const id = "c-known";
      s.applyFullHistory(id, {
        conversation_id: id,
        messages: [msg(id, 1), msg(id, 2)],
        context_window_size: 0,
        max_sequence_id: 9,
      });
      const rec = s.peek(id)!;
      assert(rec.maxSequenceIdKnown === 9, `expected 9, got ${rec.maxSequenceIdKnown}`);
      // A subsequent applyFullHistory with smaller max should not regress.
      s.applyFullHistory(id, {
        conversation_id: id,
        messages: [msg(id, 1), msg(id, 2)],
        context_window_size: 0,
        max_sequence_id: 4,
      });
      assert(s.peek(id)!.maxSequenceIdKnown === 9, "known did not regress");
      await s.settle();
    },
  );

  // ── Catch-up invariants ────────────────────────────────────────────────────

  await run("catch-up: stream reports higher seq than we have => needsBackfill", async () => {
    const s = freshStore();
    const id = "c-catch1";
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [msg(id, 1), msg(id, 2), msg(id, 3)],
      context_window_size: 0,
      max_sequence_id: 3,
    });
    s.setMaxSequenceIdKnown(id, 5);
    const rec = s.peek(id)!;
    assert(rec.maxSequenceIdKnown === 5, `known=${rec.maxSequenceIdKnown}`);
    assert(rec.maxSequenceId === 3, `local=${rec.maxSequenceId}`);
    assert(rec.maxSequenceIdKnown > rec.maxSequenceId, "known > local");
    assert(needsBackfill(rec), "needsBackfill true");
    await s.settle();
  });

  await run("catch-up: upsertMessages closes the gap", async () => {
    const s = freshStore();
    const id = "c-catch2";
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [msg(id, 1), msg(id, 2), msg(id, 3)],
      context_window_size: 0,
      max_sequence_id: 3,
    });
    s.setMaxSequenceIdKnown(id, 5);
    assert(needsBackfill(s.peek(id)), "behind before upsert");
    s.upsertMessages(id, [msg(id, 4), msg(id, 5)]);
    const rec = s.peek(id)!;
    assert(rec.maxSequenceId === 5, `local=${rec.maxSequenceId}`);
    assert(rec.maxSequenceId === rec.maxSequenceIdKnown, "local==known");
    assert(!needsBackfill(rec), "caught up");
    await s.settle();
  });

  await run("setMaxSequenceIdKnown is monotonic (high water mark wins)", async () => {
    const s = freshStore();
    const id = "c-mono";
    s.setMaxSequenceIdKnown(id, 10);
    assert(s.peek(id)!.maxSequenceIdKnown === 10, "set to 10");
    s.setMaxSequenceIdKnown(id, 5);
    assert(s.peek(id)!.maxSequenceIdKnown === 10, "5 ignored");
    // Multiple writers over the same factory: the high value must win in IDB.
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const sA = storeFor({ factory, dbName, keyId, rawKey });
    const sB = storeFor({ factory, dbName, keyId, rawKey });
    sA.setMaxSequenceIdKnown(id, 7);
    sB.setMaxSequenceIdKnown(id, 12);
    sA.setMaxSequenceIdKnown(id, 4);
    await Promise.all([sA.settle(), sB.settle()]);
    await sA.close();
    await sB.close();
    const s3 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = await s3.hydrate(id);
    assert(hyd!.maxSequenceIdKnown === 12, `persisted known=${hyd!.maxSequenceIdKnown}`);
  });

  await run("applyFullHistory after a stale stream signal clears backfill", async () => {
    const s = freshStore();
    const id = "c-stalefirst";
    s.setMaxSequenceIdKnown(id, 7);
    assert(needsBackfill(s.peek(id)), "behind before history");
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [1, 2, 3, 4, 5, 6, 7].map((n) => msg(id, n)),
      context_window_size: 0,
      max_sequence_id: 7,
    });
    const rec = s.peek(id)!;
    assert(rec.hasFullHistory, "hasFullHistory");
    assert(rec.maxSequenceId === 7, `local=${rec.maxSequenceId}`);
    assert(rec.maxSequenceIdKnown === 7, `known=${rec.maxSequenceIdKnown}`);
    assert(!needsBackfill(rec), "caught up");
    await s.settle();
  });

  await run(
    "applyFullHistory ratchets known above delivered messages => still needsBackfill",
    async () => {
      const { factory, dbName, keyId, rawKey } = freshFactory();
      const s = storeFor({ factory, dbName, keyId, rawKey });
      const id = "c-ratchet";
      s.applyFullHistory(id, {
        conversation_id: id,
        messages: [msg(id, 1), msg(id, 2), msg(id, 3)],
        context_window_size: 0,
        max_sequence_id: 10,
      });
      let rec = s.peek(id)!;
      assert(rec.maxSequenceId === 3, `local=${rec.maxSequenceId}`);
      assert(rec.maxSequenceIdKnown === 10, `known=${rec.maxSequenceIdKnown}`);
      assert(needsBackfill(rec), "behind even with full history flag");
      await s.settle();
      await s.close();
      // Verify it survives a fresh hydrate.
      const s2 = storeFor({ factory, dbName, keyId, rawKey });
      rec = (await s2.hydrate(id))!;
      assert(rec.hasFullHistory, "hasFullHistory persisted");
      assert(rec.maxSequenceId === 3, `persisted local=${rec.maxSequenceId}`);
      assert(rec.maxSequenceIdKnown === 10, `persisted known=${rec.maxSequenceIdKnown}`);
      assert(needsBackfill(rec), "behind after hydrate");
    },
  );

  await run("out-of-order: late live event then applyFullHistory does not desync", async () => {
    const s = freshStore();
    const id = "c-ooo";
    s.upsertMessages(id, [msg(id, 5)]);
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [1, 2, 3, 4, 5].map((n) => msg(id, n)),
      context_window_size: 0,
      max_sequence_id: 5,
    });
    const rec = s.peek(id)!;
    assert(rec.hasFullHistory, "hasFullHistory");
    assert(rec.maxSequenceId === 5, `local=${rec.maxSequenceId}`);
    assert(rec.messages.length === 5, `dedup len=${rec.messages.length}`);
    const ids = new Set(rec.messages.map((m) => m.message_id));
    assert(ids.size === 5, "no duplicate message_ids");
    assert(!needsBackfill(rec), "caught up");
    await s.settle();
  });

  await run("regenerated turn: same message_id at new seq dedups", async () => {
    const s = freshStore();
    const id = "c-regen";
    s.upsertMessages(id, [msg(id, 3, "x")]);
    s.upsertMessages(id, [msg(id, 7, "x")]);
    const rec = s.peek(id)!;
    assert(rec.messages.length === 1, `expected 1 row, got ${rec.messages.length}`);
    assert(rec.messages[0].message_id === "x", "message_id preserved");
    assert(rec.maxSequenceId === 7, `max=${rec.maxSequenceId}`);
    await s.settle();
  });

  await run("regenerated turn survives a reload: the moved row is not duplicated", async () => {
    // Pins the ordering constraint inside the incremental write: each row's
    // by_message_id lookup must see the state BEFORE that row's own put, or a
    // message that moved to a new sequence_id gets written twice (once at the
    // old key, once at the new). The in-memory test above can't catch that --
    // only what actually landed on disk can.
    const f = freshFactory();
    const s = storeFor(f);
    const id = "c-regen-disk";
    s.upsertMessages(id, [msg(id, 3, "x"), msg(id, 4, "y")]);
    await s.settle();
    // "x" is regenerated at a later sequence_id; "y" is untouched.
    s.upsertMessages(id, [msg(id, 9, "x")]);
    await s.settle();
    await s.close();

    // The persist path reports failures through cacheDiag rather than
    // throwing, so a botched write would otherwise look like a silent pass.
    assert(
      !cacheDiagStats()["persist.upsert_failed"],
      `the write itself must succeed: ${JSON.stringify(cacheDiagStats())}`,
    );

    const s2 = storeFor(f);
    const hyd = await s2.hydrate(id);
    assert(hyd !== null, "hydrated from disk");
    const ids = hyd!.messages.map((m) => m.message_id).sort();
    assert(
      hyd!.messages.length === 2,
      `expected 2 rows on disk, got ${hyd!.messages.length} (${ids.join(",")})`,
    );
    const x = hyd!.messages.find((m) => m.message_id === "x");
    assert(x !== undefined, "the regenerated message is present");
    assert(x!.sequence_id === 9, `it must live at its NEW seq, got ${x!.sequence_id}`);
    await s2.close();
  });

  await run("markAllStale forces a server re-check but not a full re-download", async () => {
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-reconnect";
    s.upsertMessages(id, [msg(id, 1), msg(id, 2)]);
    s.applyFullHistory(id, {
      conversation_id: id,
      messages: [msg(id, 1), msg(id, 2), msg(id, 3)],
      context_window_size: 0,
      max_sequence_id: 3,
    });
    await s.settle();
    assert(!needsBackfill(s.peek(id)), "caught up before stale");
    s.markAllStale();
    const rec = s.peek(id)!;
    assert(rec.needsRefresh, "flagged for re-verification");
    assert(needsBackfill(rec), "must talk to the server after a reconnect");
    assert(!needsFullReload(rec), "but a tail fetch suffices");
    assert(rec.messages.length === 3, "hot messages preserved for fast paint");
    await s.settle();
    await s.close();
    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = (await s2.hydrate(id))!;
    assert(hyd.messages.length === 3, "persisted messages preserved");
    assert(needsBackfill(hyd), "persisted stale => still re-checks");
    assert(!needsFullReload(hyd), "persisted stale => tail fetch suffices");
  });

  await run("per-conversation isolation: A's known does not bleed into B", async () => {
    const s = freshStore();
    s.setMaxSequenceIdKnown("a", 10);
    s.setMaxSequenceIdKnown("b", 3);
    assert(s.peek("a")!.maxSequenceIdKnown === 10, "a known=10");
    assert(s.peek("b")!.maxSequenceIdKnown === 3, "b known=3");
    await s.settle();
  });

  await run("persist-after-reload: fresh store detects it is behind", async () => {
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const id = "c-reload";
    const sA = storeFor({ factory, dbName, keyId, rawKey });
    sA.applyFullHistory(id, {
      conversation_id: id,
      messages: [msg(id, 1), msg(id, 2), msg(id, 3)],
      context_window_size: 0,
      max_sequence_id: 3,
    });
    await sA.settle();
    await sA.close();

    const sB = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = (await sB.hydrate(id))!;
    assert(!needsBackfill(hyd), "fresh hydrate caught up");
    // Stream event: someone else appended messages while we were gone.
    sB.setMaxSequenceIdKnown(id, 8);
    const rec = sB.peek(id)!;
    assert(rec.maxSequenceIdKnown === 8, `known=${rec.maxSequenceIdKnown}`);
    assert(rec.maxSequenceId === 3, `local=${rec.maxSequenceId}`);
    assert(needsBackfill(rec), "fresh store detects behind");
    await sB.settle();
  });

  await run("pruneStale drops convs not in active set and older than cutoff", async () => {
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const fresh = "c-fresh";
    const oldActive = "c-old-active";
    const oldArchived = "c-old-archived";

    // Use one store to seed all three convs, then close it.
    const sA = storeFor({ factory, dbName, keyId, rawKey });
    sA.upsertMessages(fresh, [msg(fresh, 1)]);
    sA.upsertMessages(oldActive, [msg(oldActive, 1)]);
    sA.upsertMessages(oldArchived, [msg(oldArchived, 1)]);
    await sA.settle();
    await sA.close();

    // Backdate the two "old" convs by patching the on-disk meta rows
    // directly via a raw IDB connection over the same factory.
    const tenDaysMs = 10 * 24 * 60 * 60 * 1000;
    const old = Date.now() - tenDaysMs;
    await new Promise<void>((resolve, reject) => {
      const req = factory.open(dbName);
      req.onsuccess = () => {
        const db = req.result;
        const tx = db.transaction(["conversation_meta"], "readwrite");
        const store = tx.objectStore("conversation_meta");
        for (const id of [oldActive, oldArchived]) {
          const r = store.get(id);
          r.onsuccess = () => {
            const row = r.result as { updated_at: number } | undefined;
            if (row) {
              row.updated_at = old;
              store.put(row);
            }
          };
        }
        tx.oncomplete = () => {
          db.close();
          resolve();
        };
        tx.onerror = () => reject(tx.error);
      };
      req.onerror = () => reject(req.error);
    });

    // Now open a fresh store (cold hot map) and prune. Active set = [fresh, oldActive].
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const sevenDaysMs = 7 * 24 * 60 * 60 * 1000;
    const pruned = await s.pruneStale([fresh, oldActive], sevenDaysMs);
    assert(pruned.length === 1, `expected 1 pruned, got ${pruned.length}`);
    assert(pruned[0] === oldArchived, `pruned wrong id: ${pruned[0]}`);

    // Cross-instance: oldArchived's rows are gone; fresh + oldActive remain.
    const sB = storeFor({ factory, dbName, keyId, rawKey });
    assert((await sB.hydrate(oldArchived)) === null, "oldArchived rows gone from IDB");
    assert((await sB.hydrate(fresh)) !== null, "fresh retained in IDB");
    assert((await sB.hydrate(oldActive)) !== null, "oldActive retained in IDB");
    await sB.close();
    await s.close();
  });

  await run("pruneStale re-checks staleness atomically (no race with upsert)", async () => {
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const id = "c-racy";
    const sA = storeFor({ factory, dbName, keyId, rawKey });
    sA.upsertMessages(id, [msg(id, 1)]);
    await sA.settle();
    await sA.close();

    // Backdate the meta on disk.
    const tenDaysMs = 10 * 24 * 60 * 60 * 1000;
    await new Promise<void>((resolve, reject) => {
      const req = factory.open(dbName);
      req.onsuccess = () => {
        const db = req.result;
        const tx = db.transaction(["conversation_meta"], "readwrite");
        const store = tx.objectStore("conversation_meta");
        const r = store.get(id);
        r.onsuccess = () => {
          const row = r.result as { updated_at: number } | undefined;
          if (row) {
            row.updated_at = Date.now() - tenDaysMs;
            store.put(row);
          }
        };
        tx.oncomplete = () => {
          db.close();
          resolve();
        };
        tx.onerror = () => reject(tx.error);
      };
      req.onerror = () => reject(req.error);
    });

    // Simulate a racy upsert just before the prune transaction by
    // upserting a fresh message via store A in parallel with the prune.
    // Both must settle without losing the upsert's data.
    const sB = storeFor({ factory, dbName, keyId, rawKey });
    const sevenDaysMs = 7 * 24 * 60 * 60 * 1000;
    // Kick off an upsert that will arrive concurrently. await its settle
    // before pruneStale's tx so it lands first (re-bumping updated_at).
    sB.upsertMessages(id, [msg(id, 2)]);
    await sB.settle();
    const pruned = await sB.pruneStale([], sevenDaysMs);
    assert(
      pruned.length === 0,
      `racy upsert should have saved the conv, got pruned=${pruned.length}`,
    );
    // Open a third store to verify the on-disk row was not deleted by
    // pruneStale's atomic re-check.
    await sB.close();
    const sC = storeFor({ factory, dbName, keyId, rawKey });
    const rec = await sC.hydrate(id);
    assert(rec !== null, "meta + messages survive on disk");
    assert(rec!.messages.length >= 1, `expected >=1 message on disk, got ${rec!.messages.length}`);
    await sC.close();
  });

  await run("pruneStale keeps recently-touched conversations even when archived", async () => {
    const s = freshStore();
    const id = "c-recent-archived";
    s.upsertMessages(id, [msg(id, 1)]);
    await s.settle();
    // Conversation is not in the active set, but was touched < 1ms ago.
    const pruned = await s.pruneStale([], 7 * 24 * 60 * 60 * 1000);
    assert(pruned.length === 0, `expected 0 pruned, got ${pruned.length}`);
    assert(s.peek(id) !== null, "recent archived conv retained");
  });

  // ── Encryption-at-rest ───────────────────────────────────────────────────────────

  await run("persisted message rows have no plaintext user/llm data", async () => {
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-secret";
    const m = msg(id, 1);
    const SECRET = "my-secret-conversation-text-xyz";
    (m as Message & { user_data: string | null }).user_data = SECRET;
    s.upsertMessages(id, [m]);
    await s.settle();
    await s.close();

    // Open the same IDB via the raw API and verify the row has no
    // plaintext payload — just the indexed fields + iv/ct.
    const req = factory.open(dbName, 4);
    const rawDb: IDBDatabase = await new Promise((resolve, reject) => {
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
    const rawRows: Array<Record<string, unknown>> = await new Promise((resolve, reject) => {
      const r = rawDb.transaction("messages", "readonly").objectStore("messages").getAll();
      r.onsuccess = () => resolve(r.result as Array<Record<string, unknown>>);
      r.onerror = () => reject(r.error);
    });
    rawDb.close();
    assert(rawRows.length === 1, `expected 1 raw row, got ${rawRows.length}`);
    const row = rawRows[0];
    assert(row.conversation_id === id, "plaintext conversation_id preserved");
    assert(typeof row.sequence_id === "number", "plaintext sequence_id preserved");
    assert(typeof row.message_id === "string", "plaintext message_id preserved");
    assert(row.user_data === undefined, "user_data not stored as plaintext");
    assert(row.llm_data === undefined, "llm_data not stored as plaintext");
    const ct = row.ct as Uint8Array;
    assert(ct instanceof Uint8Array && ct.byteLength > 0, "ct present");
    // Sanity: secret string should not appear anywhere in the row's
    // serialized representation.
    const decoder = new TextDecoder();
    assert(
      !decoder.decode(ct).includes(SECRET),
      "ct does not literally contain the plaintext secret",
    );
  });

  await run("persisted meta row has no plaintext Conversation payload", async () => {
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-meta";
    const TITLE = "my-private-conversation-title";
    s.setConversation(id, {
      conversation_id: id,
      slug: "x",
      title: TITLE,
    } as unknown as import("../types").Conversation);
    await s.settle();
    await s.close();
    const req = factory.open(dbName, 4);
    const rawDb: IDBDatabase = await new Promise((resolve, reject) => {
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
    const rawRows: Array<Record<string, unknown>> = await new Promise((resolve, reject) => {
      const r = rawDb
        .transaction("conversation_meta", "readonly")
        .objectStore("conversation_meta")
        .getAll();
      r.onsuccess = () => resolve(r.result as Array<Record<string, unknown>>);
      r.onerror = () => reject(r.error);
    });
    rawDb.close();
    assert(rawRows.length === 1, `expected 1 raw row, got ${rawRows.length}`);
    const row = rawRows[0];
    assert(row.conversation === undefined, "conversation not plaintext");
    assert(row.context_window_size === undefined, "ctx not plaintext");
    assert(typeof row.updated_at === "number", "updated_at plaintext");
    const ct = row.ct as Uint8Array;
    const decoder = new TextDecoder();
    assert(!decoder.decode(ct).includes(TITLE), "ct does not contain title");
  });

  await run("different key wipes prior cache on next open", async () => {
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const id = "c-rot";
    const s = storeFor({ factory, dbName, keyId, rawKey });
    s.upsertMessages(id, [msg(id, 1), msg(id, 2)]);
    await s.settle();
    await s.close();

    // Reopen with a different keyId (simulating server cookie rotation).
    const s2 = storeFor({
      factory,
      dbName,
      keyId: "different-key-id",
      rawKey: randomKey(),
    });
    const hyd = await s2.hydrate(id);
    assert(hyd === null, "prior conv unreadable after rotation");
    // And the on-disk message rows should be wiped, not just orphaned.
    const req = factory.open(dbName, 4);
    const rawDb: IDBDatabase = await new Promise((resolve, reject) => {
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
    const count: number = await new Promise((resolve, reject) => {
      const r = rawDb.transaction("messages", "readonly").objectStore("messages").count();
      r.onsuccess = () => resolve(r.result as number);
      r.onerror = () => reject(r.error);
    });
    rawDb.close();
    assert(count === 0, `expected 0 leftover rows after rotation, got ${count}`);
  });

  await run("same key across instances: cipher decrypts cleanly", async () => {
    // Fixed key shared across two stores — the second can decrypt rows
    // the first wrote, proving the wire format + IV-per-row round-trip.
    const { factory, dbName } = freshFactory();
    const keyId = "kid-shared";
    const rawKey = randomKey();
    const sA = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-share";
    sA.upsertMessages(id, [msg(id, 1), msg(id, 2), msg(id, 3)]);
    await sA.settle();
    await sA.close();
    const sB = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = await sB.hydrate(id);
    assert(hyd !== null, "hydrated");
    assert(hyd!.messages.length === 3, `got ${hyd!.messages.length}`);
  });

  await run("wipeAndRotateKey calls server clear and wipes IDB", async () => {
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const fetcher = new StaticFetcher(keyId, rawKey);
    const s = new MessageStore({
      factory,
      dbName,
      keyHolder: new CacheKeyHolder(fetcher),
    });
    const id = "c-wipe";
    s.upsertMessages(id, [msg(id, 1)]);
    await s.settle();
    await s.wipeAndRotateKey();
    assert(fetcher.wasCleared(), "server clear called");
    // After wipe, rotate the fetcher's key and re-hydrate; should be empty.
    fetcher.rotate("kid2", randomKey());
    const hyd = await s.hydrate(id);
    assert(hyd === null, "no data after wipe");
  });

  await run("AAD binds message ct to its plaintext keys (splice rejected)", async () => {
    // An attacker with IDB write access copies a valid {iv,ct} from one
    // message row onto another row's plaintext keys. Without AAD, GCM
    // would still authenticate. With per-row AAD bound to {kind,
    // conversation_id, sequence_id, message_id}, decrypt must fail.
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = storeFor({ factory, dbName, keyId, rawKey });
    const id = "c-aad";
    s.upsertMessages(id, [msg(id, 1), msg(id, 2)]);
    await s.settle();
    await s.close();

    // Splice ct/iv from row [id, 2] onto row [id, 1]'s plaintext keys.
    const req = factory.open(dbName, 4);
    const raw: IDBDatabase = await new Promise((resolve, reject) => {
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
    const rows: MessageRowLike[] = await new Promise((resolve, reject) => {
      const r = raw.transaction("messages", "readonly").objectStore("messages").getAll();
      r.onsuccess = () => resolve(r.result as MessageRowLike[]);
      r.onerror = () => reject(r.error);
    });
    const seq1 = rows.find((r) => r.sequence_id === 1)!;
    const seq2 = rows.find((r) => r.sequence_id === 2)!;
    seq1.iv = seq2.iv;
    seq1.ct = seq2.ct;
    await new Promise<void>((resolve, reject) => {
      const r = raw.transaction("messages", "readwrite").objectStore("messages").put(seq1);
      r.onsuccess = () => resolve();
      r.onerror = () => reject(r.error);
    });
    raw.close();

    // Hydrate via a fresh store — the spliced row must drop, the legit
    // one (seq 2) must survive.
    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = await s2.hydrate(id);
    assert(hyd !== null, "hydrate non-null");
    assert(hyd!.messages.length === 1, `expected 1 surviving row, got ${hyd!.messages.length}`);
    assert(hyd!.messages[0].sequence_id === 2, "surviving row is the legitimate seq=2");
  });

  await run("wipeAndRotateKey rejects when server clear fails", async () => {
    // If the server-side cache-session/clear endpoint fails, we must NOT
    // silently report success: that would leave the next /api/cache-key
    // call returning the same key_id and the wipe-on-mismatch path would
    // never fire on reload.
    class FailingFetcher implements CacheKeyFetcher {
      constructor(
        private keyId: string,
        private rawKey: Uint8Array,
      ) {}
      async fetch(): Promise<CacheKeyMaterial> {
        const buf = new ArrayBuffer(this.rawKey.byteLength);
        new Uint8Array(buf).set(this.rawKey);
        const key = await crypto.subtle.importKey("raw", buf, { name: "AES-GCM" }, false, [
          "encrypt",
          "decrypt",
        ]);
        return { keyId: this.keyId, key, alg: "AES-GCM-256" };
      }
      async clear(): Promise<void> {
        throw new Error("simulated 500");
      }
    }
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const s = new MessageStore({
      factory,
      dbName,
      keyHolder: new CacheKeyHolder(new FailingFetcher(keyId, rawKey)),
    });
    let threw = false;
    try {
      await s.wipeAndRotateKey();
    } catch {
      threw = true;
    }
    assert(threw, "wipeAndRotateKey should reject on server clear failure");
  });

  await run(
    "resetTransient preserves agentWorking from a previously-received list patch",
    async () => {
      const s = freshStore();
      const id = "c-reset-state";
      // A conversation_list_patch carrying agent_working=true arrives
      // over the global stream before the focus effect runs
      // resetTransient(id).
      s.setAgentWorking(id, true);
      s.resetTransient(id);
      assert(
        s.getTransient(id).agentWorking === true,
        "resetTransient must not clobber a live agentWorking=true",
      );
    },
  );

  await run(
    "resetTransient does not trust agent_working from a cached conversation row",
    async () => {
      // Embedded Conversation snapshots in unrelated stream events can lag
      // the latest SetConversationAgentWorking by a DB write, so the focus
      // reset must NOT seed agentWorking from rec.conversation. Sync to
      // the persistent flag happens through the authoritative
      // conversation_state / conversation_list_patch paths.
      const s = freshStore();
      const id = "c-reset-row";
      s.setConversation(id, conv(id, true));
      s.resetTransient(id);
      assert(
        s.getTransient(id).agentWorking === false,
        "resetTransient should not seed agentWorking from the cached Conversation row",
      );
    },
  );

  await run("resetTransient clears tool progress and streamed content", async () => {
    const s = freshStore();
    const id = "c-reset-ephemera";
    s.setToolProgress(id, { tool_use_id: "tool-1", tool_name: "shell", output: "x" });
    s.appendStreamText(id, "hello");
    s.appendStreamThinking(id, "hmm");
    s.setAgentWorking(id, true);
    s.resetTransient(id);
    const t = s.getTransient(id);
    assert(t.agentWorking === true, "agentWorking should still be preserved");
    assert(Object.keys(t.toolProgress).length === 0, "toolProgress should be wiped on reset");
    assert(t.streamingText === "", "streamingText should be wiped on reset");
    assert(t.streamingThinking === "", "streamingThinking should be wiped on reset");
  });

  // ── Multi-tab liveness ──────────────────────────────────────────────────────
  //
  // Everything below is the same failure shape: hydrate() is the only thing
  // between a tab and its rendered conversation, so any await inside it that
  // can block forever becomes a permanently spinning tab. Multiple Safari tabs
  // on one origin are what make these waits real (shared IDB, shared socket
  // pool, shared cookie jar).
  //
  // Deadlines are injected (tens of ms) so the give-up behaviour is asserted
  // without sleeping for the production timeout.

  /** Hold a connection at an older version, like a tab open across a deploy. */
  async function holdOldVersion(factory: IDBFactory, dbName: string): Promise<IDBDatabase> {
    const db = await new Promise<IDBDatabase>((res, rej) => {
      const req = factory.open(dbName, 1);
      req.onupgradeneeded = () => void req.result.createObjectStore("legacy");
      req.onsuccess = () => res(req.result);
      req.onerror = () => rej(req.error);
    });
    // An old build with no versionchange handler never closes on request,
    // which is what makes the block indefinite rather than momentary.
    db.onversionchange = () => {};
    return db;
  }

  await run("hydrate() gives up instead of hanging when the cache key never arrives", async () => {
    // With several tabs open on the HTTP/1.1 path, every tab holds an SSE
    // connection and the six-per-origin socket cap leaves later tabs'
    // /api/cache-key request queued indefinitely. hydrate() must degrade to
    // "no cache" so the UI falls back to the network and renders, instead of
    // awaiting a promise that never settles.
    const { factory, dbName } = freshFactory();
    const s = new MessageStore({
      factory,
      dbName,
      keyHolder: new CacheKeyHolder(
        { fetch: () => new Promise(() => {}), clear: async () => {} }, // never settles
        SHORT_TIMEOUT_MS,
      ),
    });
    try {
      const rec = await Promise.race([
        s.hydrate("c-no-key"),
        sleep(SHORT_TIMEOUT_MS * 20).then(() => "hung" as const),
      ]);
      assert(rec !== "hung", "hydrate must settle even when the key fetch never does");
      assert(rec === null, "and report a cache miss so the UI reloads from the server");
      assert(!s.isHydrated("c-no-key"), "a give-up must stay retryable, not count as hydrated");
    } finally {
      await s.close();
    }
  });

  await run("hydrate() gives up when another tab blocks the IDB upgrade", async () => {
    // A tab left open from before a deploy that bumped DB_VERSION holds the
    // old connection. openDB() at the new version fires 'blocked' and stays
    // pending for as long as that tab lives, so the new tab's spinner never
    // clears.
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const held = await holdOldVersion(factory, dbName);
    const s = storeFor({
      factory,
      dbName,
      keyId,
      rawKey,
      openTimeoutMs: SHORT_TIMEOUT_MS,
      openCooldownMs: SHORT_COOLDOWN_MS,
    });
    try {
      const rec = await Promise.race([
        s.hydrate("c-blocked"),
        sleep(SHORT_TIMEOUT_MS * 20).then(() => "hung" as const),
      ]);
      assert(rec !== "hung", "hydrate must settle even while the upgrade is blocked");
      assert(rec === null, "and report a miss so the conversation still renders from REST");
      assert(!s.isHydrated("c-blocked"), "a give-up must not be recorded as hydrated");
    } finally {
      held.close();
      await s.close();
    }
  });

  await run("a blocked open is retried once the blocking tab goes away", async () => {
    // Giving up must not be permanent: when the old tab closes, the cache has
    // to come back rather than running network-only for the rest of the
    // session.
    const { factory, dbName, keyId, rawKey } = freshFactory();
    const held = await holdOldVersion(factory, dbName);
    const s = storeFor({
      factory,
      dbName,
      keyId,
      rawKey,
      openTimeoutMs: SHORT_TIMEOUT_MS,
      openCooldownMs: SHORT_COOLDOWN_MS,
    });
    try {
      assert((await s.hydrate("c-retry")) === null, "blocked hydrate reports a miss");
      held.close();
      // Wait out the post-timeout cooldown, then prove a real write lands and
      // reads back from a second store — i.e. the DB handle recovered rather
      // than latching into a failed state.
      await sleep(SHORT_COOLDOWN_MS + 20);
      s.upsertMessages("c-retry", [msg("c-retry", 1)]);
      await s.settle();
      const s2 = storeFor({ factory, dbName, keyId, rawKey });
      try {
        const rec = await s2.hydrate("c-retry");
        assert(
          rec !== null && rec.messages.length === 1,
          "the cache works again once the blocker closes",
        );
      } finally {
        await s2.close();
      }
    } finally {
      held.close();
      await s.close();
    }
  });

  await run("hydrate survives a sibling tab holding the stores locked", async () => {
    // The hang this pins: openAndSyncKey bounds the key fetch and the DB open,
    // then takes an exclusive readwrite tx on all three stores. IDB locks are
    // origin-wide, so ANOTHER TAB mid-write blocks that tx — and because db()
    // caches its promise, the stall is permanent and tab-wide. Worse, it
    // happens before any cacheDiag call, so the tab reports stats={} and
    // waiting=[] while spinning: no evidence at all. Real Safari reproduced
    // exactly that, recovering only when the other tab's tx was released.
    const f = freshFactory();
    // Populate through a normal store so the DB exists at the right version.
    const seed = storeFor(f);
    seed.applyFullHistory("c1", {
      conversation_id: "c1",
      messages: [msg("c1", 0, "hello")],
      context_window_size: 100,
      max_sequence_id: 0,
    });
    await seed.settle();
    await seed.close();

    // A sibling holds an exclusive readwrite tx on the stores that
    // openAndSyncKey needs, and does not let go.
    const raw = await new Promise<IDBDatabase>((res, rej) => {
      const r = f.factory.open(f.dbName);
      r.onsuccess = () => res(r.result);
      r.onerror = () => rej(r.error);
    });
    const held = raw.transaction(["keys_meta", "messages", "conversation_meta"], "readwrite");
    // Keep the tx alive: chain each request inside the previous onsuccess, with
    // no macrotask gap (a setTimeout would let it auto-commit).
    let holding = true;
    const store = held.objectStore("keys_meta");
    (function spin() {
      if (!holding) return;
      const q = store.get("current");
      q.onsuccess = spin;
      q.onerror = spin;
    })();

    const s2 = storeFor({ ...f, txTimeoutMs: SHORT_TIMEOUT_MS });
    try {
      // Must not hang: the deadline turns a blocked lock into a cache miss.
      const rec = await s2.hydrate("c1");
      assert(rec === null || rec.messages.length === 0, "degrades to a miss, not a hang");
      assert(
        cacheDiagStats()["idb.tx_timeout"] > 0,
        `the give-up must be reported: ${JSON.stringify(cacheDiagStats())}`,
      );
    } finally {
      holding = false;
      try {
        held.abort();
      } catch {
        /* already ended */
      }
      raw.close();
      await s2.close();
    }
  });

  await run("the cache recovers once the sibling releases the lock", async () => {
    // A blocked open must not poison the tab: db() caches its promise, so if a
    // timed-out attempt stayed installed the cache would be dead until reload.
    const f = freshFactory();
    const seed = storeFor(f);
    seed.applyFullHistory("c1", {
      conversation_id: "c1",
      messages: [msg("c1", 0, "hello")],
      context_window_size: 100,
      max_sequence_id: 0,
    });
    await seed.settle();
    await seed.close();

    const raw = await new Promise<IDBDatabase>((res, rej) => {
      const r = f.factory.open(f.dbName);
      r.onsuccess = () => res(r.result);
      r.onerror = () => rej(r.error);
    });
    const held = raw.transaction(["keys_meta", "messages", "conversation_meta"], "readwrite");
    let holding = true;
    const store = held.objectStore("keys_meta");
    (function spin() {
      if (!holding) return;
      const q = store.get("current");
      q.onsuccess = spin;
      q.onerror = spin;
    })();

    const s2 = storeFor({ ...f, txTimeoutMs: SHORT_TIMEOUT_MS, openCooldownMs: 1 });
    await s2.hydrate("c1"); // blocked -> miss
    holding = false;
    try {
      held.abort();
    } catch {
      /* already ended */
    }
    raw.close();
    // Give the abort a turn to land, then the cache must work again.
    await sleep(SHORT_TIMEOUT_MS);
    const rec = await s2.hydrate("c1");
    assert(rec !== null, "hydrate works again once the lock is free");
    assert(rec!.messages.length === 1, `expected the cached message, got ${rec!.messages.length}`);
    await s2.close();
  });

  await run("startup is not blocked by a sibling writing a DIFFERENT conversation", async () => {
    // The real-world hang: tab A bulk-writes one conversation while tab B
    // starts up. Startup only needs to CHECK keys_meta - in the common case it
    // mutates nothing - so it must not demand a write lock spanning the data
    // stores. With the old readwrite scope, B's startup queued behind A's
    // write of an unrelated conversation and (per db()'s cached promise) stayed
    // broken for the life of the tab.
    //
    // Scoped to keys_meta only: a readonly read of `messages` would still
    // legitimately queue behind an exclusive writer on `messages`. What the fix
    // buys is that startup no longer JOINS that queue.
    const f = freshFactory();
    const seed = storeFor(f);
    seed.applyFullHistory("c1", {
      conversation_id: "c1",
      messages: [msg("c1", 0, "hello")],
      context_window_size: 100,
      max_sequence_id: 0,
    });
    await seed.settle();
    await seed.close();

    const raw = await new Promise<IDBDatabase>((res, rej) => {
      const r = f.factory.open(f.dbName);
      r.onsuccess = () => res(r.result);
      r.onerror = () => rej(r.error);
    });
    // Exactly the scope a bulk write takes: the data stores, held open.
    const held = raw.transaction(["messages", "conversation_meta"], "readwrite");
    let holding = true;
    const store = held.objectStore("conversation_meta");
    (function spin() {
      if (!holding) return;
      const q = store.get("c1");
      q.onsuccess = spin;
      q.onerror = spin;
    })();

    const s2 = storeFor({ ...f, txTimeoutMs: SHORT_TIMEOUT_MS });
    try {
      // hydrate() goes on to read `messages`, which legitimately queues behind
      // the sibling's exclusive lock on that store - IDB working as specified,
      // not the defect. So assert on WHICH wait we gave up on: startup getting
      // a usable connection must succeed; only the data read may time out.
      const before = cacheDiagStats()["idb.tx_timeout"] ?? 0;
      await s2.hydrate("c1");
      const after = cacheDiagStats()["idb.tx_timeout"] ?? 0;
      assert(
        after === before,
        `startup must not block on the sibling's write (idb.tx_timeout ${before} -> ${after})`,
      );
    } finally {
      holding = false;
      try {
        held.abort();
      } catch {
        /* already ended */
      }
      raw.close();
      await s2.close();
    }
  });

  await run("startup still wipes when the server rotated the key", async () => {
    // The readonly fast path must not cost us the wipe: rows encrypted under
    // the old key are unreadable, and leaving them would let a later read
    // surface undecryptable rows instead of a clean miss.
    const f = freshFactory();
    const seed = storeFor(f);
    seed.applyFullHistory("c1", {
      conversation_id: "c1",
      messages: [msg("c1", 0, "hello")],
      context_window_size: 100,
      max_sequence_id: 0,
    });
    await seed.settle();
    await seed.close();

    // Same DB, different key id: the server rotated.
    const rotated = new MessageStore({
      factory: f.factory,
      dbName: f.dbName,
      keyHolder: new CacheKeyHolder(new StaticFetcher("kid-rotated", randomKey())),
    });
    const rec = await rotated.hydrate("c1");
    assert(rec === null || rec.messages.length === 0, "stale rows are gone after rotation");
    const raw = await new Promise<IDBDatabase>((res, rej) => {
      const r = f.factory.open(f.dbName);
      r.onsuccess = () => res(r.result);
      r.onerror = () => rej(r.error);
    });
    const km = await new Promise<{ key_id: string } | undefined>((res, rej) => {
      const q = raw.transaction(["keys_meta"], "readonly").objectStore("keys_meta").get("current");
      q.onsuccess = () => res(q.result);
      q.onerror = () => rej(q.error);
    });
    assert(km?.key_id === "kid-rotated", `keys_meta must record the new key, got ${km?.key_id}`);
    raw.close();
    await rotated.close();
  });

  console.log("\nmessageStore tests passed");
}

/** Shape we read directly via the raw IndexedDB API for the splice test. */
interface MessageRowLike {
  conversation_id: string;
  sequence_id: number;
  message_id: string;
  iv: Uint8Array;
  ct: Uint8Array;
}

await main();
