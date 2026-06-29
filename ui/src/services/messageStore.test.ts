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
 * issues a REST backfill when the cache lacks full history OR when the
 * server-reported max sequence_id is ahead of what we have locally.
 */
function needsBackfill(rec: ConversationCacheRecord | null): boolean {
  if (!rec || !rec.hasFullHistory) return true;
  if (rec.maxSequenceIdKnown <= 0) return false;
  return rec.maxSequenceId < rec.maxSequenceIdKnown;
}

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
}): MessageStore {
  const fetcher = new StaticFetcher(fixture.keyId, fixture.rawKey);
  return new MessageStore({
    factory: fixture.factory,
    dbName: fixture.dbName,
    keyHolder: new CacheKeyHolder(fetcher),
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

async function run(name: string, fn: () => Promise<void>): Promise<void> {
  try {
    await fn();
    console.log(`✓ ${name}`);
  } catch (err) {
    console.error(`✗ ${name}`);
    throw err;
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

  await run("markAllStale preserves messages on disk", async () => {
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
    assert(s.peek(id)!.hasFullHistory === false, "hot cleared");
    assert(s.peek(id)!.messages.length === 3, "hot messages preserved");
    await s.close();

    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = await s2.hydrate(id);
    assert(hyd !== null && hyd.messages.length === 3, "messages preserved on disk");
    assert(hyd!.hasFullHistory === false, "has_full_history persisted=false");
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

  await run("markAllStale forces needsBackfill but preserves messages", async () => {
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
    assert(!rec.hasFullHistory, "hasFullHistory cleared");
    assert(needsBackfill(rec), "needsBackfill after stale");
    assert(rec.messages.length === 3, "hot messages preserved for fast paint");
    await s.settle();
    await s.close();
    const s2 = storeFor({ factory, dbName, keyId, rawKey });
    const hyd = (await s2.hydrate(id))!;
    assert(hyd.messages.length === 3, "persisted messages preserved");
    assert(needsBackfill(hyd), "persisted stale=>needsBackfill");
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

  await run("resetTransient clears toolProgress and streamingText", async () => {
    const s = freshStore();
    const id = "c-reset-ephemera";
    s.setToolProgress(id, { tool_use_id: "tool-1", tool_name: "shell", output: "x" });
    s.appendStreamDelta(id, "hello");
    s.setAgentWorking(id, true);
    s.resetTransient(id);
    const t = s.getTransient(id);
    assert(t.agentWorking === true, "agentWorking should still be preserved");
    assert(Object.keys(t.toolProgress).length === 0, "toolProgress should be wiped on reset");
    assert(t.streamingText === "", "streamingText should be wiped on reset");
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
