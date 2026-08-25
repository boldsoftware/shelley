// messageStore.ts — IndexedDB-backed per-message cache plus an in-memory
// layer for transient streaming state (tool progress, stream deltas, agent
// working). Components subscribe by conversation_id (or globally) to receive
// updates. Stream events from globalStream.ts flow in via the apply* methods.
//
// Persistence model: write-behind, but per-call atomic. Each public mutator
// updates the in-memory hot map and notifies listeners synchronously, then
// kicks off a single IDB readwrite transaction. The transaction does a true
// read-modify-write of the conversation_meta row in the same tx as the
// message puts, so concurrent writers (other tabs / store instances) cannot
// lose `max_sequence_id_local`.
//
// Completeness model — two distinct "the cache isn't authoritative" flags,
// because they warrant very different repairs:
//
//   hasFullHistory=false  We are missing messages we cannot name. Only a full
//                         GET /api/conversation/<id> can repair it.
//   needsRefresh=true     The cached prefix is complete but may be short: the
//                         server might have grown past our tail (set on stream
//                         reconnect). Repaired by a cheap
//                         ?last_sequence_id=<our max> tail fetch.
//
// Conflating them is a real bug in both directions: a tail fetch starts from
// our cached max, so it can never recover a message missing from the MIDDLE;
// and re-downloading whole conversations on every reconnect costs megabytes.
//
// A hole in the middle is reachable in ordinary use, not just on cache damage.
// /api/stream2 delivers events for ALL conversations, including ones the user
// isn't viewing: cache 1..3 for conversation B, close the tab, let 4..6 be
// written, then open a new tab where a live event delivers 7 for B before B is
// ever focused. Nothing in the sequence bookkeeping notices — local max jumps
// to 7 and typically equals the conversation list's known max, so every
// catch-up counter reports "up to date" over a hole. Hence the join checks in
// upsertMessages, _persistUpsert and mergeRecords/joinsUp, all of which clear
// hasFullHistory (never merely needsRefresh).
//
// Note we do NOT infer completeness from the sequence_id run being
// consecutive: ids legitimately have gaps. db.CopyMessagesForFork copies only
// the fork point's generation while preserving the source sequence_ids, so a
// fork can hold a complete history with holes in it. Lost rows are detected by
// row count instead (ConvMetaRow.message_count).
//
// For the same reason we do not require a cached history to START at sequence
// 1: a fork taken after a compaction legitimately begins well past it. What a
// full REST snapshot establishes is that whatever it returned IS the whole
// history. The one exception is an EMPTY snapshot with live rows grafted on
// top, which establishes nothing — see applyFullHistory.
//
// At-rest encryption (v4): the sensitive payload of each row is AES-GCM
// encrypted with a per-browser key derived server-side from a long-lived
// secret + a session cookie (see services/cryptoKey.ts + server/cache_key.go).
// On open we fetch the key; if the server returns a different key_id than
// the one stored in `meta`, the DB is wiped before use (treats unrelated
// cipher-text as garbage). If the server refuses to release the key (auth
// lost), IDB persistence silently degrades to a no-op — the in-memory hot
// map and the live SSE stream still work; we just don't have offline cache
// until re-auth.
//
// DB schema v4:
//   messages          — keyPath [conversation_id, sequence_id]. Row =
//                       { conversation_id, sequence_id, message_id, iv, ct }.
//                       conversation_id, sequence_id, message_id stay
//                       plaintext (they are keys / indexed). The Message
//                       JSON lives inside ct.
//   conversation_meta — keyPath conversation_id. Row = { conversation_id,
//                       updated_at, iv, ct }. The rest of the meta
//                       (Conversation, sequence bookmarks, …) lives in ct.
//                       updated_at stays plaintext so pruneStale can scan.
//   keys_meta         — singleton row keyed by "current" holding { key_id }.
//                       Server key_id mismatch triggers a full wipe.

import { openDB, IDBPDatabase, IDBPObjectStore, DBSchema, OpenDBCallbacks } from "idb";
import type { Message, Conversation, StreamResponse, ToolProgress } from "../types";
import {
  CacheKeyHolder,
  HttpCacheKeyFetcher,
  wrapJSON,
  unwrapJSON,
  rowAAD,
  type CacheKeyMaterial,
} from "./cryptoKey";
import { perfCount } from "../utils/perf";
import { cacheDiag } from "./cacheDiag";
import { withDeadline, isDeadlineExceeded } from "./deadline";

// Cross-tab notification channel for key rotation. When one tab runs
// wipeAndRotateKey() the others must drop their cached CryptoKey and
// reopen the DB so they don't keep writing old-key ciphertext into a
// store that's been re-keyed under them. Optional chaining for
// environments without BroadcastChannel (older Safari / tests).
const ROTATE_CHANNEL = "shelley-cache-rotate";
type RotateMsg = { type: "rotated" };

const DEFAULT_DB_NAME = "shelley-messages";
const DB_VERSION = 4;

/**
 * How long to wait for indexedDB.open() before giving up on the cache for
 * this attempt.
 *
 * IndexedDB has no timeout of its own: if another tab still holds a
 * connection at a lower version, our open fires `blocked` and then waits —
 * indefinitely if that tab never closes its connection. A Safari window left
 * open across a deploy that bumped DB_VERSION is exactly that tab, and because
 * hydrate() awaits the open (and ChatInterface holds its spinner until hydrate
 * returns), the new tab sits on "Loading conversation…" forever with no error
 * anywhere.
 *
 * Bounding the wait turns an indefinite hang into a cache miss: the
 * conversation loads from the server instead.
 */
const IDB_OPEN_TIMEOUT_MS = 5_000;

/**
 * How long to stop trying after an open times out.
 *
 * An IDB open request cannot be cancelled, so every timed-out attempt leaves
 * a live request queued against a database we already gave up on. Without a
 * cooldown, a blocking tab means every hydrate and every write-behind task
 * piles on another one — a stampede that all wakes at once when the blocker
 * finally closes, and a flood of duplicate diagnostics in the meantime.
 */
const IDB_OPEN_COOLDOWN_MS = 30_000;

/**
 * Deadline for the startup readwrite transaction in openAndSyncKey.
 *
 * IndexedDB locks are ORIGIN-wide, not tab-wide: an exclusive readwrite tx on
 * these stores blocks every other tab's tx on them. A sibling that is slow, or
 * suspended by the OS mid-transaction, therefore stalls this tab's startup —
 * and since db() caches its promise, that stall is permanent and poisons every
 * later cache read. Observed in real Safari: a tab spinning forever with
 * stats={} and no in-flight waits, recovering only when the other tab let go.
 *
 * Shorter than the open deadline: waiting on a lock some other tab holds is
 * less likely to resolve than waiting on our own open, and the fallback (skip
 * the cache, use the network) is cheap.
 */
const IDB_TX_TIMEOUT_MS = 3_000;

/**
 * Rows per Promise.all batch for bulk crypto.subtle calls. Awaiting each row
 * individually serializes on the main-thread/crypto-thread round-trip:
 * measured on a 21k-message conversation, sequential decrypt took 610ms on
 * Safari 26.4 (1.0s on Chromium) vs 117ms batched — batches of 512 are within
 * noise of fully parallel while bounding peak in-flight promises.
 */
const CRYPTO_BATCH = 512;

/**
 * Deadline options for a cache READ.
 *
 * Reads take shared locks, but still queue behind another tab's exclusive
 * writer, so they need the same bound as the startup transaction. A late
 * result is just discarded: hydrate has already returned, and the caller has
 * gone to the network.
 */
function readDeadlineOpts(store: string) {
  return {
    what: `indexedDB.read ${store}`,
    onLate: () => cacheDiag("info", "idb.read_late", { store }),
    onLateError: (err: unknown) =>
      cacheDiag("fail", "idb.read_late_error", { store, error: String(err) }),
  };
}

// ─── IDB schema ─────────────────────────────────────────────────────────────
//
// Persisted rows are encrypted-at-rest. Plaintext fields are limited to
// what we need for indexing / range queries / pruning.

/** One row per message in the `messages` store. */
interface MessageRow {
  conversation_id: string;
  sequence_id: number;
  message_id: string;
  iv: Uint8Array;
  ct: Uint8Array;
}

/** Encrypted plaintext payload stored in MessageRow.ct. Just the Message. */
type MessagePayload = Message;

/**
 * One row per conversation in the `conversation_meta` store.
 *
 * Plaintext-on-disk fields are limited to bookkeeping integers + the
 * timestamp pruneStale needs. The sensitive payload (Conversation,
 * context_window_size) lives in `ct`.
 *
 * Why bookkeeping is plaintext: the original design uses a single RW
 * transaction to RMW these fields with Math.max so concurrent writers
 * don't regress them. IDB transactions auto-commit when control yields
 * to a non-IDB promise, and AES-GCM via crypto.subtle returns promises.
 * Decrypting/encrypting inside the tx would race the auto-commit. Keeping
 * the ratchet fields out of `ct` preserves the atomic-RMW property
 * without forcing us to decrypt the existing row inside the tx.
 */
interface ConvMetaRow {
  conversation_id: string;
  /** Kept plaintext so pruneStale can scan without decrypting. */
  updated_at: number;
  /** Server-reported maximum sequence_id (from stream or list response). */
  max_sequence_id_known: number;
  /** Highest sequence_id we have locally cached. */
  max_sequence_id_local: number;
  /** True once a full REST GET has been merged in successfully. */
  has_full_history: boolean;
  /**
   * How many message rows this conversation had when has_full_history was
   * last established, so hydrate can tell "the server's history genuinely
   * looks like this" from "we lost rows".
   *
   * We cannot infer completeness from the sequence_id run: ids are NOT
   * guaranteed consecutive. db.CopyMessagesForFork copies only the fork
   * point's generation while preserving the source sequence_ids, so a
   * forked conversation legitimately has holes where the abandoned
   * generation's messages used to be.
   *
   * Optional: rows written before this field existed read back undefined,
   * which disables the check rather than failing closed (an older row is
   * not evidence of damage).
   */
  message_count?: number;
  /**
   * True when something happened that may have changed history behind our
   * back (a stream reconnect), so the next focus must re-check with the
   * server. Distinct from has_full_history=false: the cached prefix is
   * still believed complete and renderable, we just don't know about the
   * tail, so the refresh can be an incremental `?last_sequence_id=` fetch
   * instead of re-downloading the whole conversation.
   *
   * Optional: rows written before this field existed read back undefined,
   * which is treated as false.
   */
  needs_refresh?: boolean;
  iv: Uint8Array;
  ct: Uint8Array;
}

/** Encrypted payload stored in ConvMetaRow.ct. */
interface ConvMetaPayload {
  conversation: Conversation | null;
  context_window_size: number;
}

/** Singleton row in keys_meta. */
interface KeyMetaRow {
  id: "current";
  key_id: string;
}

interface ShelleyDB extends DBSchema {
  messages: {
    key: [string, number];
    value: MessageRow;
    indexes: {
      by_message_id: string;
    };
  };
  conversation_meta: {
    key: string;
    value: ConvMetaRow;
  };
  keys_meta: {
    key: string;
    value: KeyMetaRow;
  };
}

/**
 * Narrow type for the keys_meta object store handle passed to
 * verifyKeyInTx. We accept any tx variant (the tx may include other
 * stores) so the helper can be reused from every write path.
 */
type IDBPObjectStoreForVerify = IDBPObjectStore<
  ShelleyDB,
  ("messages" | "conversation_meta" | "keys_meta")[],
  "keys_meta",
  "readwrite"
>;

function emptyPayload(): ConvMetaPayload {
  return { conversation: null, context_window_size: 0 };
}

// ─── Public in-memory aggregate shape ────────────────────────────────────────

/** In-memory aggregate returned by peek(). NOT the IDB row shape. */
export interface ConversationCacheRecord {
  conversation_id: string;
  messages: Message[];
  conversation: Conversation | null;
  contextWindowSize: number;
  minSequenceId: number;
  maxSequenceId: number;
  /** Server-reported max sequence_id (from stream events or conversation list). */
  maxSequenceIdKnown: number;
  hasFullHistory: boolean;
  /** See ConvMetaRow.needs_refresh. */
  needsRefresh: boolean;
  updatedAt: number;
}

// ─── Transient (non-persisted) state ─────────────────────────────────────────

export interface TransientState {
  toolProgress: Record<string, ToolProgress>;
  streamingText: string;
  streamingThinking: string;
  agentWorking: boolean;
}

function emptyTransient(): TransientState {
  return { toolProgress: {}, streamingText: "", streamingThinking: "", agentWorking: false };
}

function emptyRecord(id: string): ConversationCacheRecord {
  return {
    conversation_id: id,
    messages: [],
    conversation: null,
    contextWindowSize: 0,
    minSequenceId: 0,
    maxSequenceId: -1,
    maxSequenceIdKnown: 0,
    hasFullHistory: false,
    needsRefresh: false,
    updatedAt: Date.now(),
  };
}

/**
 * Merge a record read from IDB with whatever is already in the hot map.
 *
 * Both sides can legitimately hold data the other lacks: the disk row has
 * the history from previous sessions, while the hot record may already
 * hold live stream messages plus fresher metadata (conversation row,
 * context window, known-max) that landed before this conversation was
 * first focused. `hot` wins for scalar metadata (it is never older than
 * disk within a session); messages are unioned by message_id.
 */
function mergeRecords(
  disk: ConversationCacheRecord,
  hot: ConversationCacheRecord,
): ConversationCacheRecord {
  const byMsgId = new Map<string, Message>();
  for (const m of disk.messages) byMsgId.set(m.message_id, m);
  for (const m of hot.messages) byMsgId.set(m.message_id, m);
  const messages = Array.from(byMsgId.values()).sort((a, b) => a.sequence_id - b.sequence_id);
  return {
    conversation_id: disk.conversation_id,
    messages,
    conversation: hot.conversation ?? disk.conversation,
    contextWindowSize: hot.contextWindowSize || disk.contextWindowSize,
    minSequenceId: messages.length > 0 ? messages[0].sequence_id : 0,
    maxSequenceId: messages.length > 0 ? messages[messages.length - 1].sequence_id : -1,
    maxSequenceIdKnown: Math.max(disk.maxSequenceIdKnown, hot.maxSequenceIdKnown),
    hasFullHistory: (disk.hasFullHistory || hot.hasFullHistory) && joinsUp(disk, hot),
    needsRefresh: disk.needsRefresh || hot.needsRefresh,
    updatedAt: Math.max(disk.updatedAt, hot.updatedAt),
  };
}

/**
 * Whether live messages already in the hot map continue the disk row's
 * history rather than skipping over messages we never saw.
 *
 * The dangerous case: a previous session cached 1..5, messages 6..9 were
 * added while no tab was open, and now a live event delivers 10 before this
 * conversation is first focused. Merging blindly would leave a hole in the
 * middle while every counter (local max, known max) says we're caught up,
 * so the user would silently see a conversation missing messages.
 *
 * Only the JOIN is checked, not the whole run: gaps inside the disk row
 * itself can be genuine (see ConvMetaRow.message_count). Live messages
 * append, so the first one past the disk tail must be exactly tail + 1.
 */
function joinsUp(disk: ConversationCacheRecord, hot: ConversationCacheRecord): boolean {
  const known = new Set(disk.messages.map((m) => m.message_id));
  let firstNew = Infinity;
  for (const m of hot.messages) {
    if (known.has(m.message_id)) continue;
    if (m.sequence_id > disk.maxSequenceId && m.sequence_id < firstNew) firstNew = m.sequence_id;
  }
  if (firstNew === Infinity) return true;
  return firstNew === (disk.messages.length > 0 ? disk.maxSequenceId + 1 : 1);
}

function convRange(id: string): IDBKeyRange {
  return IDBKeyRange.bound([id, Number.NEGATIVE_INFINITY], [id, Number.POSITIVE_INFINITY]);
}

type Listener = () => void;

// ─── MessageStore ─────────────────────────────────────────────────────────────

export interface MessageStoreOptions {
  dbName?: string;
  /**
   * Custom IDBFactory used in place of the global `indexedDB`. The `idb`
   * library reads `globalThis.indexedDB` directly, so when this is
   * provided we temporarily swap the global during openDB(). Tests use
   * this; production callers shouldn't need to.
   */
  factory?: IDBFactory;
  /**
   * Crypto key provider. Defaults to the production HTTP fetcher hitting
   * /api/cache-key. Tests inject a deterministic in-memory holder.
   */
  keyHolder?: CacheKeyHolder;
  /**
   * Override the indexedDB.open deadline. Tests use it to assert the give-up
   * behaviour without sleeping for the production timeout; production callers
   * omit it.
   */
  openTimeoutMs?: number;
  /** Override IDB_OPEN_COOLDOWN_MS. Tests only. */
  openCooldownMs?: number;
  /** Override IDB_TX_TIMEOUT_MS. Tests only. */
  txTimeoutMs?: number;
}

export class MessageStore {
  private readonly dbName: string;
  private readonly factory: IDBFactory | undefined;
  private readonly keyHolder: CacheKeyHolder;
  private readonly openTimeoutMs: number;
  private readonly openCooldownMs: number;
  private readonly txTimeoutMs: number;
  private dbPromise: Promise<IDBPDatabase<ShelleyDB>> | null = null;
  /**
   * When indexedDB.open() last blew its deadline; 0 if it hasn't. Drives the
   * IDB_OPEN_COOLDOWN_MS backoff.
   */
  private openTimedOutAt = 0;
  private hot = new Map<string, ConversationCacheRecord>();
  private transient = new Map<string, TransientState>();
  /**
   * Conversations whose IDB row has been read (or proven absent) — i.e.
   * `hot` is authoritative for them. Only hydrate() and the paths that
   * make the disk read unnecessary (applyFullHistory: we just replaced the
   * whole row) may add to this set. Notably NOT the metadata mutators:
   * App pumps setMaxSequenceIdKnown/setConversation for every conversation
   * in the list on page load, and marking those hydrated made the very
   * first focus skip the disk read and full-REST-reload instead.
   */
  private hydrated = new Set<string>();
  private listenersById = new Map<string, Set<Listener>>();
  private transientListenersById = new Map<string, Set<Listener>>();
  private allListeners = new Set<Listener>();
  /** Pending write-behind operations. `settle()` awaits these. */
  private inflight = new Set<Promise<unknown>>();
  /**
   * Tail of the serialized write-behind chain per conversation, keyed by
   * conversation_id. Every persister that read-modify-writes the encrypted
   * meta payload has to snapshot the on-disk row, decrypt it, merge, and
   * re-encrypt OUTSIDE the IDB transaction (awaiting crypto.subtle inside a tx
   * lets the tx auto-commit), so concurrent persisters each read the same
   * pre-write snapshot and the last one to commit silently drops the other's
   * field. globalStream fires upsertMessages() + setContextWindowSize() back
   * to back for a single stream frame, which hit exactly that: the slower
   * upsert (it also encrypts every message row) overwrote the just-committed
   * context window size with 0, so the next page load hydrated 0. Chaining the
   * writes makes each RMW observe the previous one.
   *
   * ALL write-behind persisters go through queueWrite() — not just the
   * payload-touching ones — so writes are totally ordered per conversation and
   * a new persister can't reintroduce the race by forgetting to opt in. Reads
   * (hydrate) are deliberately NOT chained: they must not wait behind a slow
   * encrypt, and a read that misses a still-queued write still sees the value
   * through the hot record, which is updated synchronously. Entries are removed
   * when the chain drains (see queueWrite), so the map holds at most one entry
   * per conversation with a write in flight.
   */
  private writeChains = new Map<string, Promise<unknown>>();
  /**
   * Conversations with a hydrate() in flight, and whether markAllStale ran
   * while it was in flight. A cold hydrate builds its record from the disk
   * row, so a reconnect landing mid-read would otherwise be overwritten by
   * the pre-reconnect on-disk value and the conversation would never
   * re-check with the server.
   */
  private hydrating = new Map<string, { staleWhileHydrating: boolean }>();
  /** Cross-tab rotation channel; null when BroadcastChannel is unavailable. */
  private rotateChannel: BroadcastChannel | null = null;

  constructor(opts: MessageStoreOptions = {}) {
    this.dbName = opts.dbName ?? DEFAULT_DB_NAME;
    this.factory = opts.factory ?? (typeof indexedDB !== "undefined" ? indexedDB : undefined);
    this.keyHolder = opts.keyHolder ?? new CacheKeyHolder(new HttpCacheKeyFetcher());
    this.openTimeoutMs = opts.openTimeoutMs ?? IDB_OPEN_TIMEOUT_MS;
    this.openCooldownMs = opts.openCooldownMs ?? IDB_OPEN_COOLDOWN_MS;
    this.txTimeoutMs = opts.txTimeoutMs ?? IDB_TX_TIMEOUT_MS;
    if (typeof BroadcastChannel !== "undefined") {
      this.rotateChannel = new BroadcastChannel(ROTATE_CHANNEL);
      // Node implements BroadcastChannel via libuv and keeps the event
      // loop alive while a channel is open. Tests that forget to call
      // close() (and CI itself, which runs many test files in one node
      // invocation) would hang at exit. Browsers don't have unref() but
      // also don't have a notion of "keep process alive".
      const ch = this.rotateChannel as BroadcastChannel & { unref?: () => void };
      if (typeof ch.unref === "function") ch.unref();
      this.rotateChannel.onmessage = (ev: MessageEvent<RotateMsg>) => {
        if (ev.data?.type !== "rotated") return;
        // Another tab rotated the server key. Drop our cached CryptoKey
        // and our DB handle so the next op re-fetches a fresh key and
        // re-opens the DB (which will then run wipe-on-mismatch via
        // openAndSyncKey if our keys_meta is stale). Don't pre-emptively
        // wipe IDB here: the rotating tab's own clear() already did.
        this.keyHolder.forget();
        if (this.dbPromise) {
          this.dbPromise.then((db) => db.close()).catch(() => {});
          this.dbPromise = null;
        }
        this.hydrated.clear();
      };
    }
  }

  /** Get the current cache key, or null if the server won't release it. */
  private async getKey(): Promise<CacheKeyMaterial | null> {
    return this.keyHolder.ensure();
  }

  // ── DB open ────────────────────────────────────────────────────────────────

  private db(): Promise<IDBPDatabase<ShelleyDB>> {
    if (!this.factory) return Promise.reject(new Error("indexedDB unavailable"));
    if (!this.dbPromise) {
      this.dbPromise = this.openAndSyncKey().catch((err) => {
        this.dbPromise = null;
        throw err;
      });
    }
    return this.dbPromise;
  }

  /**
   * Open the DB, then reconcile its stored key_id against the server's
   * current one. Mismatch → wipe before returning (the prior cipher-text
   * is unreadable). If the server refuses to give us a key, the open
   * fails so callers fall back to memory-only.
   */
  private async openAndSyncKey(): Promise<IDBPDatabase<ShelleyDB>> {
    const material = await this.getKey();
    if (!material) throw new Error("messageStore: cache key unavailable");
    const db = await this.openWithFactory();
    try {
      // Bounded: this tx takes an EXCLUSIVE lock on all three stores, and IDB
      // locks are origin-wide. A sibling tab mid-write (or suspended by the OS
      // holding a tx open) blocks us here indefinitely, before any cacheDiag
      // call has run — the tab spins with stats={} and nothing to explain it.
      // Give up and let the caller fall back to the network instead.
      await withDeadline(this.syncKeyInTx(db, material), this.txTimeoutMs, {
        what: "indexedDB.startup transaction",
        // The tx is uncancellable, so it may still commit after we walk away.
        // That is harmless: it only records/wipes for a key we did fetch, and
        // the next db() re-reads whatever it settled on.
        onLate: () => cacheDiag("info", "idb.tx_late", {}),
        onLateError: (err) => cacheDiag("fail", "idb.tx_late_error", { error: String(err) }),
      });
    } catch (err) {
      // Close so we don't strand a connection that would block the next
      // version bump, and so the retry starts from a clean handle.
      db.close();
      if (isDeadlineExceeded(err)) {
        cacheDiag("fail", "idb.tx_timeout", { timeout_ms: this.txTimeoutMs });
      }
      throw err;
    }
    return db;
  }

  /**
   * Reconcile the DB's recorded key_id against the one the server just gave
   * us, wiping unreadable rows. Split out of openAndSyncKey so the whole
   * transaction can be raced against a deadline as one unit.
   */
  private async syncKeyInTx(
    db: IDBPDatabase<ShelleyDB>,
    material: CacheKeyMaterial,
  ): Promise<void> {
    // Fast path: just CHECK the recorded key. In the overwhelmingly common
    // case (nothing rotated) startup mutates nothing, so demanding a write
    // lock over messages + conversation_meta would serialize every tab's
    // startup behind any sibling that happens to be bulk-writing. A readonly
    // tx takes a SHARED lock, so concurrent readers don't block each other,
    // and it doesn't overlap the data stores at all.
    const existing = await db.get("keys_meta", "current");
    if (existing?.key_id === material.keyId) return;

    // Slow path: we must actually mutate, so take the exclusive lock. Re-read
    // inside the tx rather than trusting the readonly result — another tab may
    // have claimed the key in between, and acting on the stale read would wipe
    // rows that tab just wrote under the very same key.
    const tx = db.transaction(["keys_meta", "messages", "conversation_meta"], "readwrite");
    const km = tx.objectStore("keys_meta");
    const current = await km.get("current");
    if (current?.key_id === material.keyId) {
      // Someone else recorded our key while we were escalating. Nothing to do.
      await tx.done;
      return;
    }
    if (!current) {
      // No recorded key. Defensive: if there are pre-existing rows
      // (from a process that crashed mid-rotation, a stale upgrade,
      // or a malicious write), they cannot belong to the current key
      // — wipe before claiming ownership. Otherwise just record.
      const msgsCount = await tx.objectStore("messages").count();
      const metaCount = await tx.objectStore("conversation_meta").count();
      if (msgsCount > 0 || metaCount > 0) {
        await tx.objectStore("messages").clear();
        await tx.objectStore("conversation_meta").clear();
      }
      await km.put({ id: "current", key_id: material.keyId });
    } else {
      // Server rotated; old rows are useless. Wipe.
      await tx.objectStore("messages").clear();
      await tx.objectStore("conversation_meta").clear();
      await km.put({ id: "current", key_id: material.keyId });
    }
    await tx.done;
  }

  private async openWithFactory(): Promise<IDBPDatabase<ShelleyDB>> {
    const callbacks: OpenDBCallbacks<ShelleyDB> = {
      upgrade(db, oldVersion) {
        // Drop old v1 "conversations" store if present (cache only — no data loss).
        if (db.objectStoreNames.contains("conversations" as never)) {
          db.deleteObjectStore("conversations" as never);
        }
        // v2/v3 had plaintext rows. v4 changes the row shape to { iv, ct }.
        // Drop both stores wholesale so we don't try to decrypt plaintext.
        if (db.objectStoreNames.contains("messages")) {
          db.deleteObjectStore("messages");
        }
        if (db.objectStoreNames.contains("conversation_meta")) {
          db.deleteObjectStore("conversation_meta");
        }
        const msgStore = db.createObjectStore("messages", {
          keyPath: ["conversation_id", "sequence_id"],
        });
        msgStore.createIndex("by_message_id", "message_id", { unique: true });
        db.createObjectStore("conversation_meta", {
          keyPath: "conversation_id",
        });
        if (!db.objectStoreNames.contains("keys_meta")) {
          db.createObjectStore("keys_meta", { keyPath: "id" });
        }
        void oldVersion;
      },
      // Another tab requested an upgrade — close and forget the cached
      // connection so the next db() call reopens at the new version.
      blocking: (_oldVersion, _newVersion, event) => {
        const target = event.target as IDBPDatabase<ShelleyDB> | null;
        if (target) target.close();
        this.dbPromise = null;
      },
      // We are the one being blocked: an older tab still holds a lower-version
      // connection. Transient in the common case (the other tab closes), so
      // not a "fail" — the timeout below is what we alarm on.
      blocked: (currentVersion, blockedVersion) => {
        cacheDiag("info", "idb.open_blocked", {
          current_version: currentVersion,
          wanted_version: blockedVersion,
        });
      },
    };
    if (this.openTimedOutAt !== 0 && Date.now() - this.openTimedOutAt < this.openCooldownMs) {
      // Still inside the cooldown from a previous timeout. Fail immediately
      // rather than queueing yet another uncancellable open request against a
      // database we already know is blocked.
      throw new Error("messageStore: indexedDB open is blocked (cooling down)");
    }
    const globalFactory = typeof indexedDB !== "undefined" ? indexedDB : undefined;
    if (this.factory === globalFactory) {
      return this.openWithDeadline(() => openDB<ShelleyDB>(this.dbName, DB_VERSION, callbacks));
    }
    // Test path: a custom factory was injected. `idb` reads
    // `globalThis.indexedDB` directly, so temporarily swap it.
    const g = globalThis as { indexedDB?: IDBFactory };
    const prev = g.indexedDB;
    g.indexedDB = this.factory;
    try {
      return await this.openWithDeadline(() =>
        openDB<ShelleyDB>(this.dbName, DB_VERSION, callbacks),
      );
    } finally {
      g.indexedDB = prev;
    }
  }

  /**
   * Open with a deadline, disposing of a connection that arrives after we gave
   * up.
   *
   * The late close matters for more than tidiness: an abandoned connection at
   * the current version would itself block the NEXT version bump, turning one
   * stale tab into a permanently stuck origin.
   */
  private async openWithDeadline(
    open: () => Promise<IDBPDatabase<ShelleyDB>>,
  ): Promise<IDBPDatabase<ShelleyDB>> {
    try {
      const db = await withDeadline(open(), this.openTimeoutMs, {
        what: "indexedDB.open",
        onLate: (late) => {
          cacheDiag("info", "idb.open_late", { closed: true });
          try {
            late.close();
          } catch {
            /* already closed */
          }
          // It opened, so whatever was blocking us has gone. Ending the
          // cooldown early lets the next hydrate pick the cache back up
          // instead of staying network-only for the rest of the window.
          this.openTimedOutAt = 0;
        },
        // A genuine failure (quota, corruption, VersionError) that lands after
        // the deadline must not vanish: "the cache stopped working and nothing
        // said why" is the exact failure mode this file works to prevent.
        onLateError: (err) => cacheDiag("fail", "idb.open_late_error", { error: String(err) }),
      });
      this.openTimedOutAt = 0;
      return db;
    } catch (err) {
      if (isDeadlineExceeded(err)) {
        this.openTimedOutAt = Date.now();
        cacheDiag("fail", "idb.open_timeout", {
          timeout_ms: this.openTimeoutMs,
          cooldown_ms: this.openCooldownMs,
        });
      }
      throw err;
    }
  }

  /** Close (and forget) the underlying connection. Tests use this; also
   * releases the BroadcastChannel so per-test stores don't leak channel
   * subscriptions across tests. */
  async close(): Promise<void> {
    await this.settle();
    if (this.rotateChannel) {
      this.rotateChannel.close();
      this.rotateChannel = null;
    }
    if (!this.dbPromise) return;
    try {
      const db = await this.dbPromise;
      db.close();
    } catch {
      // ignore
    } finally {
      this.dbPromise = null;
    }
  }

  /** Wait until all write-behind operations have completed. */
  async settle(): Promise<void> {
    while (this.inflight.size > 0) {
      const pending = Array.from(this.inflight);
      await Promise.allSettled(pending);
    }
  }

  /** Track a write-behind promise so `settle()` can await it. */
  private track<T>(p: Promise<T>): Promise<T> {
    this.inflight.add(p);
    const done = () => {
      this.inflight.delete(p);
    };
    p.then(done, done);
    return p;
  }

  /**
   * Run a write-behind operation after all previously queued writes for the
   * same conversation have finished, and track it so `settle()` awaits it.
   * A failed write does not break the chain: successors still run (the
   * rejection handler is the op itself, so `then(op, op)` runs it either way).
   *
   * Only the tail is retained, and only until it settles — an op that never
   * settles pins its entry, which is also what would keep `settle()` hanging.
   */
  private queueWrite(id: string, op: () => Promise<void>): Promise<void> {
    const prev = this.writeChains.get(id);
    const p = (prev ? prev.then(op, op) : op()).finally(() => {
      // Only clear the chain if nothing was queued behind us.
      if (this.writeChains.get(id) === p) this.writeChains.delete(id);
    });
    this.writeChains.set(id, p);
    return this.track(p);
  }

  // ── Encrypted row helpers ──────────────────────────────────────────────
  //
  // wrapXxx is sync-ish (awaits subtle.encrypt); unwrapXxx never throws
  // — a decrypt failure (wrong key / corrupt) is logged and treated as
  // if the row didn't exist. That makes us robust against partial-wipe
  // scenarios where keys_meta says one key but a stray row was written
  // under another (shouldn't happen, but defensive).

  /**
   * AAD bound into every encrypted message row. Authenticates (but does
   * not encrypt) the plaintext key fields so an attacker with IDB write
   * access cannot splice a valid {iv,ct} blob from one row onto another
   * row's keys. Decrypt will fail closed if any of these don't match.
   */
  private messageAAD(m: { conversation_id: string; sequence_id: number; message_id: string }) {
    return rowAAD({
      kind: "msg",
      conversation_id: m.conversation_id,
      sequence_id: m.sequence_id,
      message_id: m.message_id,
    });
  }

  /** AAD for a conversation_meta row. */
  private metaAAD(conversation_id: string) {
    return rowAAD({ kind: "meta", conversation_id });
  }

  private async encryptMessageRow(key: CryptoKey, m: Message): Promise<MessageRow> {
    const { iv, ct } = await wrapJSON(key, m, this.messageAAD(m));
    return {
      conversation_id: m.conversation_id,
      sequence_id: m.sequence_id,
      message_id: m.message_id,
      iv,
      ct,
    };
  }

  private async decryptMessageRow(key: CryptoKey, row: MessageRow): Promise<Message | null> {
    try {
      return await unwrapJSON<MessagePayload>(key, row.iv, row.ct, this.messageAAD(row));
    } catch (err) {
      cacheDiag(
        "fail",
        "decrypt.message_row_failed",
        { conversation_id: row.conversation_id, message_id: row.message_id, error: String(err) },
        row.conversation_id,
      );
      return null;
    }
  }

  private async decryptMetaRow(key: CryptoKey, row: ConvMetaRow): Promise<ConvMetaPayload | null> {
    try {
      return await unwrapJSON<ConvMetaPayload>(
        key,
        row.iv,
        row.ct,
        this.metaAAD(row.conversation_id),
      );
    } catch (err) {
      cacheDiag(
        "fail",
        "decrypt.meta_row_failed",
        { conversation_id: row.conversation_id, error: String(err) },
        row.conversation_id,
      );
      return null;
    }
  }

  /**
   * Re-check inside an open write tx that the keys_meta singleton still
   * names the same key_id our caller is about to write under. Other tabs
   * may have rotated the key between the time we encrypted (outside any
   * tx, since subtle.encrypt would auto-commit) and the time the tx
   * actually runs. If we wrote anyway the new-key store would acquire
   * old-key ciphertext that survives wipe-on-mismatch (because keys_meta
   * already names the new key). Returns true if it's safe to proceed.
   *
   * Must be called from inside a tx that includes the "keys_meta" store.
   */
  private async verifyKeyInTx(
    km: IDBPObjectStoreForVerify,
    expectedKeyId: string,
  ): Promise<boolean> {
    const cur = await km.get("current");
    if (!cur || cur.key_id !== expectedKeyId) {
      // Key was rotated under us. Drop the in-memory key so the next op
      // re-fetches from the server.
      this.keyHolder.forget();
      this.dbPromise?.then((db) => db.close()).catch(() => {});
      this.dbPromise = null;
      return false;
    }
    return true;
  }

  // ── Hydrate ────────────────────────────────────────────────────────────────

  /**
   * Load a conversation from IDB into the hot cache if not already loaded.
   *
   * Anything already in the hot map (live stream messages, metadata from the
   * conversation list) is merged with, not overwritten by, the disk row —
   * see mergeRecords. Returns the resulting record, or null when there is
   * nothing cached for this conversation.
   *
   * On a cache-key outage or an IDB failure the conversation is left
   * un-hydrated so a later call can retry; every such path is reported
   * through cacheDiag so "the cache silently stopped working" is visible in
   * the console instead of only as extra network traffic.
   */
  async hydrate(id: string): Promise<ConversationCacheRecord | null> {
    if (this.hydrated.has(id)) {
      return this.hot.get(id) ?? null;
    }
    // Track the in-flight read so a markAllStale() landing while we're
    // reading isn't lost when we install the disk-derived record.
    const inFlight = this.hydrating.get(id) ?? { staleWhileHydrating: false };
    this.hydrating.set(id, inFlight);
    try {
      return await this._hydrate(id, inFlight);
    } finally {
      this.hydrating.delete(id);
    }
  }

  private async _hydrate(
    id: string,
    inFlight: { staleWhileHydrating: boolean },
  ): Promise<ConversationCacheRecord | null> {
    let rec: ConversationCacheRecord | null = null;
    let undecryptable = 0;
    try {
      const material = await this.getKey();
      if (!material) {
        // No key: IDB is unreadable for now (auth blip / server refusal).
        // Do NOT mark hydrated — a retry once the key comes back must be
        // able to pick the cache up.
        cacheDiag("fail", "hydrate.no_cache_key", { conversation_id: id }, id);
        return this.hot.get(id) ?? null;
      }
      const db = await this.db();
      // Bounded: these are readonly (shared) locks, but a sibling tab holding
      // an EXCLUSIVE lock on the same store still blocks them for as long as
      // it likes — including forever, if that tab was suspended mid-write.
      // Reading the cache must never outlast just fetching from the network.
      const meta = await withDeadline(
        db.get("conversation_meta", id),
        this.txTimeoutMs,
        readDeadlineOpts("conversation_meta"),
      );
      if (meta) {
        const payload = await this.decryptMetaRow(material.key, meta);
        if (payload) {
          // getAll on the compound key range returns rows in ascending
          // (conv, seq) order — no JS sort needed.
          const rows = await withDeadline(
            db.getAll("messages", convRange(id)),
            this.txTimeoutMs,
            readDeadlineOpts("messages"),
          );
          const decrypted: Message[] = [];
          for (let i = 0; i < rows.length; i += CRYPTO_BATCH) {
            const batch = await Promise.all(
              rows.slice(i, i + CRYPTO_BATCH).map((r) => this.decryptMessageRow(material.key, r)),
            );
            for (const m of batch) {
              if (m) decrypted.push(m);
              else undecryptable++;
            }
          }
          const minSeq = decrypted.length > 0 ? decrypted[0].sequence_id : 0;
          const maxSeq = decrypted.length > 0 ? decrypted[decrypted.length - 1].sequence_id : -1;
          // Trust has_full_history only if we read back as many rows as were
          // written. A short read means rows were lost (undecryptable, a
          // partial wipe, a prune race) and the cached history has a hole,
          // so it must be repaired rather than rendered as complete.
          //
          // Row COUNT, not sequence contiguity: sequence_ids legitimately
          // have gaps (see ConvMetaRow.message_count). Rows written before
          // message_count existed leave it undefined — treat that as "no
          // evidence of damage" so an older cache still works.
          const lostRows =
            typeof meta.message_count === "number" && decrypted.length < meta.message_count;
          rec = {
            conversation_id: id,
            messages: decrypted,
            conversation: payload.conversation,
            contextWindowSize: payload.context_window_size,
            minSequenceId: minSeq,
            maxSequenceId: maxSeq,
            maxSequenceIdKnown: meta.max_sequence_id_known,
            hasFullHistory: meta.has_full_history && !lostRows,
            needsRefresh: !!meta.needs_refresh,
            updatedAt: meta.updated_at,
          };
          if (undecryptable > 0) {
            cacheDiag(
              "fail",
              "hydrate.undecryptable_rows",
              { conversation_id: id, dropped: undecryptable, kept: decrypted.length },
              id,
            );
          }
          if (lostRows) {
            cacheDiag(
              "fail",
              "hydrate.rows_missing",
              { conversation_id: id, expected: meta.message_count, got: decrypted.length },
              id,
            );
          }
        } else {
          cacheDiag("fail", "hydrate.undecryptable_meta", { conversation_id: id }, id);
        }
      } else {
        cacheDiag("info", "hydrate.miss", { conversation_id: id });
      }
    } catch (err) {
      // IDB is unavailable (private mode, quota, corrupt db, another tab
      // blocking an upgrade). Leave it retryable and be loud once.
      cacheDiag("fail", "hydrate.idb_error", { conversation_id: id, error: String(err) }, id);
      return this.hot.get(id) ?? null;
    }
    this.hydrated.add(id);
    const hot = this.hot.get(id);
    if (rec && hot) {
      // Live data (stream messages, list metadata) landed before this
      // conversation was first focused. Union it with the disk row.
      rec = mergeRecords(rec, hot);
    }
    if (rec) {
      // A stream reconnect during the read applies to the record we're about
      // to install — the disk row predates the reconnect, so its
      // needs_refresh cannot reflect it.
      if (inFlight.staleWhileHydrating && !rec.needsRefresh) {
        rec.needsRefresh = true;
        this.queueWrite(id, () => this._patchMeta(id, { needs_refresh: true })).catch((err) =>
          cacheDiag("fail", "persist.mark_stale_failed", { error: String(err) }, id),
        );
      }
      this.hot.set(id, rec);
      this.notify(id);
      cacheDiag("hit", hot ? "hydrate.merged" : "hydrate.loaded", {
        conversation_id: id,
        messages: rec.messages.length,
        full: rec.hasFullHistory,
      });
    } else {
      rec = hot ?? null;
    }
    return rec;
  }

  // ── Peek / isHydrated ──────────────────────────────────────────────────────

  peek(id: string): ConversationCacheRecord | null {
    return this.hot.get(id) ?? null;
  }

  isHydrated(id: string): boolean {
    return this.hydrated.has(id);
  }

  // ── Transient ──────────────────────────────────────────────────────────────

  getTransient(id: string): TransientState {
    let t = this.transient.get(id);
    if (!t) {
      t = emptyTransient();
      this.transient.set(id, t);
    }
    return t;
  }

  // ── needsBackfill ──────────────────────────────────────────────────────────

  needsBackfill(id: string): boolean {
    const rec = this.hot.get(id);
    return !rec || !rec.hasFullHistory;
  }

  // ── clearNeedsRefresh ──────────────────────────────────────────────────────

  /**
   * Mark a conversation as re-verified against the server. Called after an
   * incremental tail fetch confirms we're caught up, so the next focus can
   * serve straight from cache again.
   */
  clearNeedsRefresh(id: string): void {
    const rec = this.hot.get(id);
    if (!rec || !rec.needsRefresh) return;
    rec.needsRefresh = false;
    rec.updatedAt = Date.now();
    this.hot.set(id, rec);
    this.notify(id);
    this.queueWrite(id, () => this._patchMeta(id, { needs_refresh: false })).catch((err) =>
      cacheDiag("fail", "persist.clear_needs_refresh_failed", { error: String(err) }, id),
    );
  }

  // ── upsertMessages ─────────────────────────────────────────────────────────

  /** Merge a batch of messages into the per-conv cache (streaming upsert). */
  upsertMessages(id: string, incoming: Message[]): void {
    if (incoming.length === 0) return;
    perfCount("store.upsertMessages");
    const rec = this.hot.get(id) ?? emptyRecord(id);
    const byMsgId = new Map<string, Message>();
    for (const m of rec.messages) byMsgId.set(m.message_id, m);
    // An append must continue the history we already hold. If the first
    // genuinely-new message skips past our tail, messages were committed
    // while we weren't listening (dropped SSE frames, a burst delivered to a
    // conversation we weren't subscribed to) and the cache now has a hole in
    // the middle. Crucially, that hole would otherwise be INVISIBLE: local
    // max jumps to the new tail and typically matches the list's known max,
    // so every catch-up counter would report "up to date". Clearing
    // hasFullHistory forces the next focus into a FULL reload — a
    // ?last_sequence_id= tail fetch can never recover a missing middle.
    const prevMax = rec.maxSequenceId;
    let firstNewSeq = Infinity;
    for (const m of incoming) {
      if (!byMsgId.has(m.message_id) && m.sequence_id > prevMax && m.sequence_id < firstNewSeq) {
        firstNewSeq = m.sequence_id;
      }
    }
    if (
      rec.hasFullHistory &&
      firstNewSeq !== Infinity &&
      firstNewSeq !== (rec.messages.length > 0 ? prevMax + 1 : 1)
    ) {
      rec.hasFullHistory = false;
      cacheDiag(
        "fail",
        "upsert.sequence_skip",
        { conversation_id: id, cached_max: prevMax, first_new: firstNewSeq },
        id,
      );
    }
    for (const m of incoming) byMsgId.set(m.message_id, m);

    // Rebuild sorted array (dedup by message_id, sort by sequence_id).
    const merged = Array.from(byMsgId.values()).sort((a, b) => a.sequence_id - b.sequence_id);
    rec.messages = merged;
    if (merged.length > 0) {
      rec.minSequenceId = merged[0].sequence_id;
      rec.maxSequenceId = merged[merged.length - 1].sequence_id;
    }
    rec.updatedAt = Date.now();
    this.hot.set(id, rec);
    // NB: no this.hydrated.add(id) — a live stream message for a
    // conversation we haven't focused yet must not suppress the disk read.
    // hydrate() merges the two (mergeRecords).
    this.notify(id);

    // Snapshot what to persist; do not rely on hot record mutating between now
    // and when the tx runs.
    const snapshotIncoming = incoming.slice();
    const snapshotKnown = rec.maxSequenceIdKnown;
    const snapshotConv = rec.conversation;
    const snapshotCtx = rec.contextWindowSize;
    this.queueWrite(id, () =>
      this._persistUpsert(id, snapshotIncoming, snapshotKnown, snapshotConv, snapshotCtx),
    ).catch((err) =>
      cacheDiag("fail", "persist.upsert_failed", { conversation_id: id, error: String(err) }, id),
    );
  }

  private async _persistUpsert(
    id: string,
    incoming: Message[],
    knownHint: number,
    convHint: Conversation | null,
    ctxHint: number,
  ): Promise<void> {
    const material = await this.getKey();
    if (!material) return;
    // Encrypt OUTSIDE the IDB tx — crypto.subtle returns promises and
    // awaiting non-IDB promises inside a tx invalidates it. The encrypted
    // payload for the meta row depends on the *existing* row; we read it
    // in its own RX tx first (snapshot), encrypt, then do a single RW tx
    // that does the true RMW of the plaintext ratchet fields.
    const encRows: MessageRow[] = [];
    for (let i = 0; i < incoming.length; i += CRYPTO_BATCH) {
      encRows.push(
        ...(await Promise.all(
          incoming.slice(i, i + CRYPTO_BATCH).map((m) => this.encryptMessageRow(material.key, m)),
        )),
      );
    }
    const db = await this.db();
    // Snapshot existing meta payload for `conversation` and
    // `context_window_size` defaults. These are not ratcheted; if a
    // concurrent writer landed something fresher, our overwrite of the
    // payload is acceptable (same loose semantics as v3).
    const existingRow = await db.get("conversation_meta", id);
    const existingPayload = existingRow
      ? await this.decryptMetaRow(material.key, existingRow)
      : null;
    const payload: ConvMetaPayload = {
      conversation: convHint ?? existingPayload?.conversation ?? null,
      context_window_size:
        existingPayload?.context_window_size && existingPayload.context_window_size > 0
          ? existingPayload.context_window_size
          : ctxHint,
    };
    const { iv, ct } = await wrapJSON(material.key, payload, this.metaAAD(id));

    // Now a single RW tx — no non-IDB awaits inside.
    const tx = db.transaction(["messages", "conversation_meta", "keys_meta"], "readwrite");
    if (!(await this.verifyKeyInTx(tx.objectStore("keys_meta"), material.keyId))) {
      tx.abort();
      return;
    }
    const msgs = tx.objectStore("messages");
    const metaStore = tx.objectStore("conversation_meta");
    const existing = await metaStore.get(id);
    const prevMax = existing?.max_sequence_id_local ?? -1;
    let maxLocal = prevMax;
    let stillFull = existing?.has_full_history ?? false;
    // Expected row count carried forward from whenever has_full_history was
    // established. It must be RATCHETED by the number of genuinely new
    // message_ids we add — never reset to the observed count, or a lost row
    // plus a later append would launder an incomplete cache into one that
    // claims to be complete (the count would match again while a message is
    // missing from the middle).
    const expected = existing?.message_count;
    if (stillFull && typeof expected === "number") {
      const observedBefore = await msgs.count(convRange(id));
      if (observedBefore < expected) {
        // Rows went missing since we last claimed full history.
        stillFull = false;
      }
    }
    let added = 0;
    let firstNewSeq = Infinity;
    const idIdx = msgs.index("by_message_id");
    // Three phases instead of one interleaved loop, so the exclusive lock is
    // held across a handful of event-loop turns rather than one per message.
    // Awaiting per row makes the transaction advance only as fast as this tab
    // is scheduled; a tab the OS suspends mid-loop keeps its origin-wide lock
    // and stalls every sibling.
    //
    // The phases must stay ordered: every lookup reads the state BEFORE any of
    // this batch's puts, which is what makes a regenerated turn (same
    // message_id at a new sequence_id) a move rather than a duplicate.
    const priorKeys = await Promise.all(incoming.map((m) => idIdx.getKey(m.message_id)));
    const moved: [string, number][] = [];
    for (let i = 0; i < incoming.length; i++) {
      const m = incoming[i];
      const priorKey = priorKeys[i];
      if (priorKey) {
        if (priorKey[0] !== m.conversation_id || priorKey[1] !== m.sequence_id) {
          // Same message re-keyed to a new sequence_id (regenerated turn):
          // a move, not an addition.
          moved.push(priorKey);
        }
      } else {
        added++;
        if (m.sequence_id > prevMax && m.sequence_id < firstNewSeq) firstNewSeq = m.sequence_id;
      }
      if (m.sequence_id > maxLocal) maxLocal = m.sequence_id;
    }
    // Deletes before puts: a moved row's old key must go before its new one
    // lands, or the unique by_message_id index would see both at once.
    await Promise.all(moved.map((k) => msgs.delete(k)));
    await Promise.all(encRows.map((r) => msgs.put(r)));
    // A live append must continue the history we already hold. If it skips
    // ahead, messages were committed while we weren't listening and the
    // cached set now has a hole — mirror mergeRecords.joinsUp().
    if (stillFull && firstNewSeq !== Infinity && firstNewSeq !== (prevMax >= 0 ? prevMax + 1 : 1)) {
      stillFull = false;
    }
    const observedAfter = await msgs.count(convRange(id));
    const metaRow: ConvMetaRow = {
      conversation_id: id,
      updated_at: Date.now(),
      max_sequence_id_known: Math.max(
        existing?.max_sequence_id_known ?? 0,
        knownHint,
        maxLocal < 0 ? 0 : maxLocal,
      ),
      max_sequence_id_local: maxLocal,
      has_full_history: stillFull,
      // Ratchet while we still claim full history; otherwise record reality so
      // the next applyFullHistory starts from a truthful baseline.
      message_count: stillFull && typeof expected === "number" ? expected + added : observedAfter,
      needs_refresh: existing?.needs_refresh ?? false,
      iv,
      ct,
    };
    await metaStore.put(metaRow);
    await tx.done;
  }

  // ── applyIncrementalTail ─────────────────────────────────────────────────

  /**
   * Merge the response of an incremental `?last_sequence_id=N` fetch.
   *
   * Only valid when the cache already held a contiguous history up to N —
   * the caller (ChatInterface) checks that. `hasFullHistory` therefore
   * survives, and `needsRefresh` clears because the server just told us
   * everything past N.
   */
  applyIncrementalTail(id: string, response: StreamResponse, fromSeq: number): void {
    const rec = this.hot.get(id);
    if (!rec) {
      // The record vanished while the fetch was in flight (delete / prune /
      // cache wipe). The response covers only sequence_ids > fromSeq, so it
      // is NOT a full history — record it as a partial upsert and let the
      // next focus do a real backfill rather than marking it complete.
      cacheDiag("fail", "refresh.record_vanished", { conversation_id: id, from: fromSeq }, id);
      const incoming = (response.messages ?? []).filter((m) => m.sequence_id > fromSeq);
      if (incoming.length > 0) this.upsertMessages(id, incoming);
      return;
    }
    const incoming = (response.messages ?? []).filter((m) => m.sequence_id > fromSeq);
    cacheDiag("hit", "refresh.incremental", {
      conversation_id: id,
      from_sequence_id: fromSeq,
      new_messages: incoming.length,
    });
    if (response.conversation) rec.conversation = response.conversation;
    if (typeof response.context_window_size === "number" && response.context_window_size > 0) {
      rec.contextWindowSize = response.context_window_size;
    }
    rec.needsRefresh = false;
    this.hot.set(id, rec);
    if (incoming.length > 0) {
      // upsertMessages persists the rows, ratchets the sequence bookkeeping
      // and notifies subscribers.
      this.upsertMessages(id, incoming);
    } else {
      this.notify(id);
    }
    this.queueWrite(id, () =>
      this._patchMeta(id, { needs_refresh: false, conversation: rec.conversation }),
    ).catch((err) =>
      cacheDiag(
        "fail",
        "persist.incremental_failed",
        { conversation_id: id, error: String(err) },
        id,
      ),
    );
  }

  // ── applyFullHistory ───────────────────────────────────────────────────────

  /**
   * Apply a full REST history snapshot to the cache. The snapshot is
   * authoritative for the sequence range it covers, but it is NOT a blind
   * wholesale replace: any locally-cached messages newer than the snapshot's
   * tail (delivered live after the snapshot was taken) are preserved. See the
   * inline note for the staleness race this guards against.
   */
  applyFullHistory(id: string, response: StreamResponse): void {
    const responseMessages = (response.messages ?? [])
      .slice()
      .sort((a, b) => a.sequence_id - b.sequence_id);
    const existing = this.hot.get(id);

    // The REST snapshot is authoritative for the range it covers, but it can
    // be STALE relative to live data: loadMessages may have issued the fetch
    // before an agent turn was committed (e.g. on a brand-new conversation),
    // and under load that request can resolve only after the live stream has
    // already delivered the newer messages into the cache. Blindly replacing
    // the cache with such a snapshot would REGRESS the view, dropping messages
    // the user already (correctly) saw. So merge: keep every locally-cached
    // message whose sequence_id is beyond the snapshot's tail. (Append-only
    // sequence ids make "beyond the tail" the right boundary; messages within
    // the snapshot's range are taken from the authoritative snapshot.)
    const responseMaxSeq =
      responseMessages.length > 0 ? responseMessages[responseMessages.length - 1].sequence_id : -1;
    const newerLocal = (existing?.messages ?? []).filter((m) => m.sequence_id > responseMaxSeq);
    let messages = responseMessages;
    if (newerLocal.length > 0) {
      const byMsgId = new Map<string, Message>();
      for (const m of responseMessages) byMsgId.set(m.message_id, m);
      for (const m of newerLocal) byMsgId.set(m.message_id, m);
      messages = Array.from(byMsgId.values()).sort((a, b) => a.sequence_id - b.sequence_id);
    }
    const minSeq = messages.length > 0 ? messages[0].sequence_id : 0;
    const maxSeq = messages.length > 0 ? messages[messages.length - 1].sequence_id : -1;
    const responseKnown =
      typeof response.max_sequence_id === "number" ? response.max_sequence_id : 0;
    const knownAfter = Math.max(
      existing?.maxSequenceIdKnown ?? 0,
      responseKnown,
      maxSeq < 0 ? 0 : maxSeq,
    );
    // A full GET returns the conversation's whole history, so whatever the
    // snapshot starts at IS the start — forks legitimately begin well past
    // sequence 1 (CopyMessagesForFork preserves source ids). But an EMPTY
    // snapshot certifies nothing: when we graft locally-cached rows on top of
    // one, those rows are only a complete history if they start at sequence 1.
    //
    // Draft promotion hits exactly that. The server flips is_draft and
    // announces it, then creates the system prompt as sequence 1 WITHOUT
    // broadcasting it (see convo.go createSystemPrompt), then streams the
    // user's message as sequence 2. A refetch racing that window returns zero
    // messages while seq 2 is already hot, so certifying would permanently
    // hide the system prompt in this browser — upsertMessages' join check
    // can't catch it, because seq 2 arrived BEFORE the certification, not
    // after. Refusing here keeps the cache honestly incomplete so the next
    // focus repairs it via a full REST load.
    const certifiesFullHistory =
      responseMessages.length > 0 || messages.length === 0 || minSeq === 1;
    const rec: ConversationCacheRecord = {
      conversation_id: id,
      messages,
      conversation: response.conversation ?? existing?.conversation ?? null,
      contextWindowSize: response.context_window_size ?? existing?.contextWindowSize ?? 0,
      minSequenceId: minSeq,
      maxSequenceId: maxSeq,
      maxSequenceIdKnown: knownAfter,
      hasFullHistory: certifiesFullHistory,
      needsRefresh: false,
      updatedAt: Date.now(),
    };
    this.hot.set(id, rec);
    // Safe to claim hydrated here: _persistFullHistory replaces the whole
    // conversation's rows, so there is nothing left on disk to read.
    this.hydrated.add(id);
    this.notify(id);

    this.queueWrite(id, () => this._persistFullHistory(id, rec)).catch((err) =>
      cacheDiag(
        "fail",
        "persist.full_history_failed",
        { conversation_id: id, error: String(err) },
        id,
      ),
    );
  }

  private async _persistFullHistory(id: string, rec: ConversationCacheRecord): Promise<void> {
    const material = await this.getKey();
    if (!material) return;
    // Encrypt all message rows + the meta payload OUTSIDE the IDB tx.
    const encMsgs: MessageRow[] = [];
    for (let i = 0; i < rec.messages.length; i += CRYPTO_BATCH) {
      encMsgs.push(
        ...(await Promise.all(
          rec.messages
            .slice(i, i + CRYPTO_BATCH)
            .map((m) => this.encryptMessageRow(material.key, m)),
        )),
      );
    }
    const db = await this.db();
    const existingRow = await db.get("conversation_meta", id);
    const existingPayload = existingRow
      ? await this.decryptMetaRow(material.key, existingRow)
      : null;
    const payload: ConvMetaPayload = {
      conversation: rec.conversation ?? existingPayload?.conversation ?? null,
      context_window_size: rec.contextWindowSize,
    };
    const { iv, ct } = await wrapJSON(material.key, payload, this.metaAAD(id));

    const tx = db.transaction(["messages", "conversation_meta", "keys_meta"], "readwrite");
    if (!(await this.verifyKeyInTx(tx.objectStore("keys_meta"), material.keyId))) {
      tx.abort();
      return;
    }
    const msgs = tx.objectStore("messages");
    const metaStore = tx.objectStore("conversation_meta");
    const existing = await metaStore.get(id);
    // Replace semantics: drop everything for this conversation, then bulk put.
    await msgs.delete(convRange(id));
    // Issue every put in ONE turn and await them together, rather than
    // awaiting each in sequence. Awaiting per row makes the transaction
    // advance only as fast as this tab is scheduled, so it holds its
    // exclusive lock across thousands of event-loop turns — and a tab the OS
    // suspends mid-loop keeps that lock, which is origin-wide, stalling every
    // sibling. Measured ~3.5x less lock time on a busy main thread in Safari.
    // Safe because the rows are independent: no put depends on another's
    // result, and the tx still commits (or aborts) atomically.
    await Promise.all(encMsgs.map((r) => msgs.put(r)));
    const rowCount = await msgs.count(convRange(id));
    const row: ConvMetaRow = {
      conversation_id: id,
      updated_at: Date.now(),
      max_sequence_id_known: Math.max(existing?.max_sequence_id_known ?? 0, rec.maxSequenceIdKnown),
      // Ratchet against any concurrent writer that pushed local higher.
      max_sequence_id_local: Math.max(existing?.max_sequence_id_local ?? -1, rec.maxSequenceId),
      // Mirror the in-memory record rather than hardcoding true: an empty
      // snapshot with grafted live rows is not a complete history (see
      // applyFullHistory), and persisting `true` here would let the next
      // reload hydrate the certification we just refused.
      has_full_history: rec.hasFullHistory,
      message_count: rowCount,
      // A full REST snapshot IS the re-verification, so clear any pending
      // reconnect-driven refresh flag.
      needs_refresh: false,
      iv,
      ct,
    };
    await metaStore.put(row);
    await tx.done;
  }

  // ── setConversation ────────────────────────────────────────────────────────

  setConversation(id: string, conv: Conversation): void {
    const rec = this.hot.get(id) ?? emptyRecord(id);
    rec.conversation = conv;
    rec.updatedAt = Date.now();
    this.hot.set(id, rec);
    this.notify(id);
    this.queueWrite(id, () => this._patchMeta(id, { conversation: conv })).catch((err) =>
      cacheDiag(
        "fail",
        "persist.conversation_failed",
        { conversation_id: id, error: String(err) },
        id,
      ),
    );
  }

  // ── setContextWindowSize ───────────────────────────────────────────────────

  setContextWindowSize(id: string, size: number): void {
    const rec = this.hot.get(id) ?? emptyRecord(id);
    if (rec.contextWindowSize === size) return;
    rec.contextWindowSize = size;
    rec.updatedAt = Date.now();
    this.hot.set(id, rec);
    this.notify(id);
    this.queueWrite(id, () => this._patchMeta(id, { context_window_size: size })).catch((err) =>
      cacheDiag("fail", "persist.ctx_size_failed", { conversation_id: id, error: String(err) }, id),
    );
  }

  // ── setMaxSequenceIdKnown ──────────────────────────────────────────────────

  /**
   * Update the server-reported max sequence_id for a conversation.
   * Called by globalStream when StreamResponse.max_sequence_id > 0,
   * and by App when the conversation list is loaded or patched.
   */
  setMaxSequenceIdKnown(id: string, maxSeq: number): void {
    if (maxSeq <= 0) return;
    const rec = this.hot.get(id) ?? emptyRecord(id);
    if (rec.maxSequenceIdKnown >= maxSeq) return;
    rec.maxSequenceIdKnown = maxSeq;
    rec.updatedAt = Date.now();
    this.hot.set(id, rec);
    this.notify(id);
    this.queueWrite(id, () => this._patchMeta(id, { max_sequence_id_known: maxSeq })).catch((err) =>
      cacheDiag(
        "fail",
        "persist.known_max_failed",
        { conversation_id: id, error: String(err) },
        id,
      ),
    );
  }

  /**
   * Read-modify-write patch of a conversation_meta row. Ratcheting fields
   * (max_sequence_id_known, max_sequence_id_local) use Math.max against the
   * persisted value so a concurrent writer cannot regress them.
   *
   * Two paths:
   *   - Patches touching only plaintext bookkeeping (max_sequence_id_*,
   *     has_full_history): the existing row's iv+ct are reused inside the
   *     tx, so the whole RMW is atomic vs other writers. This is what
   *     setMaxSequenceIdKnown and markAllStale hit — they are the only
   *     paths that fire on every stream event so they must stay atomic.
   *   - Patches touching the encrypted payload (conversation,
   *     context_window_size): we snapshot+decrypt+re-encrypt outside the
   *     tx (because crypto.subtle awaits would auto-commit the tx).
   *     setConversation / setContextWindowSize fire at most once per
   *     server-pushed conversation update, so last-write-wins between
   *     concurrent payload patches is acceptable.
   */
  private async _patchMeta(
    id: string,
    patch: {
      conversation?: Conversation | null;
      context_window_size?: number;
      max_sequence_id_known?: number;
      max_sequence_id_local?: number;
      has_full_history?: boolean;
      needs_refresh?: boolean;
    },
  ): Promise<void> {
    const material = await this.getKey();
    if (!material) return;
    const touchesPayload =
      patch.conversation !== undefined || patch.context_window_size !== undefined;
    const db = await this.db();

    // For payload-touching patches: snapshot the existing payload, merge,
    // and pre-encrypt outside the tx (subtle.encrypt awaits would
    // auto-commit a readwrite tx). For bookkeeping-only patches: skip
    // the snapshot so the tx body is pure-IDB and atomic vs concurrent
    // RMWs from other tabs. We always pre-encrypt an empty payload as a
    // fallback in case `existing` is null inside the tx.
    let payloadCipher: { iv: Uint8Array; ct: Uint8Array } | null = null;
    if (touchesPayload) {
      const existingRow = await db.get("conversation_meta", id);
      const existingPayload = existingRow
        ? await this.decryptMetaRow(material.key, existingRow)
        : null;
      const basePayload: ConvMetaPayload = existingPayload ?? emptyPayload();
      const newPayload: ConvMetaPayload = {
        conversation:
          patch.conversation !== undefined ? patch.conversation : basePayload.conversation,
        context_window_size:
          patch.context_window_size !== undefined
            ? patch.context_window_size
            : basePayload.context_window_size,
      };
      payloadCipher = await wrapJSON(material.key, newPayload, this.metaAAD(id));
    }
    // Cheap empty-payload cipher in case the row doesn't exist yet and
    // we're a bookkeeping-only patch; cached neither (different IV per
    // call) so paths that don't need it pay nothing extra.
    const emptyCipher = touchesPayload
      ? null
      : await wrapJSON(material.key, emptyPayload(), this.metaAAD(id));

    const tx = db.transaction(["conversation_meta", "keys_meta"], "readwrite");
    if (!(await this.verifyKeyInTx(tx.objectStore("keys_meta"), material.keyId))) {
      tx.abort();
      return;
    }
    const store = tx.objectStore("conversation_meta");
    const existing = await store.get(id);
    let iv: Uint8Array;
    let ct: Uint8Array;
    if (payloadCipher) {
      ({ iv, ct } = payloadCipher);
    } else if (existing) {
      // Bookkeeping-only patch on an existing row: reuse iv+ct verbatim.
      // Whole RMW is inside this single tx — atomic vs other writers.
      iv = existing.iv;
      ct = existing.ct;
    } else {
      // Bookkeeping-only patch on a never-seen conv (e.g.
      // setMaxSequenceIdKnown from a list patch before backfill). Use
      // the pre-encrypted empty payload.
      ({ iv, ct } = emptyCipher!);
    }
    const baseMeta = existing ?? {
      max_sequence_id_known: 0,
      max_sequence_id_local: -1,
      has_full_history: false,
      message_count: undefined as number | undefined,
      needs_refresh: false,
    };
    const row: ConvMetaRow = {
      conversation_id: id,
      updated_at: Date.now(),
      max_sequence_id_known:
        patch.max_sequence_id_known !== undefined
          ? Math.max(baseMeta.max_sequence_id_known, patch.max_sequence_id_known)
          : baseMeta.max_sequence_id_known,
      max_sequence_id_local:
        patch.max_sequence_id_local !== undefined
          ? Math.max(baseMeta.max_sequence_id_local, patch.max_sequence_id_local)
          : baseMeta.max_sequence_id_local,
      has_full_history:
        patch.has_full_history !== undefined ? patch.has_full_history : baseMeta.has_full_history,
      // Carried through verbatim: this is a metadata-only patch, so the
      // message rows (and therefore their count) are untouched. Dropping it
      // would silently disable hydrate's damage check.
      message_count: baseMeta.message_count,
      needs_refresh:
        patch.needs_refresh !== undefined ? patch.needs_refresh : !!baseMeta.needs_refresh,
      iv,
      ct,
    };
    await store.put(row);
    await tx.done;
  }

  // ── Transient helpers ──────────────────────────────────────────────────────

  setToolProgress(id: string, p: ToolProgress): void {
    const t = this.getTransient(id);
    t.toolProgress = { ...t.toolProgress, [p.tool_use_id]: p };
    this.notifyTransient(id);
  }

  clearToolProgress(id: string, toolUseIds: string[]): void {
    if (toolUseIds.length === 0) return;
    const t = this.getTransient(id);
    let changed = false;
    const next = { ...t.toolProgress };
    for (const k of toolUseIds) {
      if (k in next) {
        delete next[k];
        changed = true;
      }
    }
    if (!changed) return;
    t.toolProgress = next;
    this.notifyTransient(id);
  }

  appendStreamText(id: string, text: string): void {
    if (!text) return;
    const t = this.getTransient(id);
    t.streamingText = t.streamingText + text;
    this.notifyTransient(id);
  }

  appendStreamThinking(id: string, text: string): void {
    if (!text) return;
    const t = this.getTransient(id);
    t.streamingThinking = t.streamingThinking + text;
    this.notifyTransient(id);
  }

  resetStreaming(id: string): void {
    const t = this.getTransient(id);
    if (!t.streamingText && !t.streamingThinking) return;
    t.streamingText = "";
    t.streamingThinking = "";
    this.notifyTransient(id);
  }

  setAgentWorking(id: string, working: boolean): void {
    const t = this.getTransient(id);
    if (t.agentWorking === working) return;
    t.agentWorking = working;
    this.notifyTransient(id);
  }

  resetTransient(id: string): void {
    // Don't blow away agentWorking — it mirrors the persistent server flag
    // (conversations.agent_working) and is authoritative across the
    // lifetime of the conversation, not per-session transient.
    //
    // Tool progress and streamed content, on the other hand, are stream-only
    // ephemera that don't survive a tab switch / refresh and would be
    // misleading if carried across a focus change.
    //
    // Seed agentWorking from the cached conversation row when available so
    // switching into a working conversation immediately reflects the
    // indicator, even if no live conversation_state event has been seen
    // since this tab was loaded.
    // Preserve the live transient flag: conversation_list_patch events
    // update agentWorking out of band and may have arrived before this
    // focus switch (e.g. for a brand-new conversation the patch landing
    // the new row beats ChatInterface's focus effect). We do NOT seed
    // from the cached Conversation row: embedded Conversation snapshots
    // in unrelated stream events can lag the latest agent_working
    // transition by one DB write, so trusting the row could re-introduce
    // the dark indicator bug. The list-patch stream is the single
    // authoritative source for the persistent flag (globalStream no
    // longer mirrors conversation_state.working into the store, since
    // those events race the list patches and can stomp a fresh value
    // with a stale one) and already pumps it into transient.
    const prev = this.transient.get(id);
    const working = !!prev?.agentWorking;
    this.transient.set(id, { ...emptyTransient(), agentWorking: working });
    this.notifyTransient(id);
  }

  // ── markAllStale ───────────────────────────────────────────────────────────

  /**
   * Mark every cached conversation as needing a server re-check.
   *
   * Called after a global-stream reconnect: while we were disconnected any
   * conversation could have grown, so the next focus must talk to the
   * server. It sets `needsRefresh` rather than clearing `hasFullHistory`
   * because the cached prefix is still complete and renderable — the UI can
   * paint from cache immediately and issue a cheap incremental
   * `?last_sequence_id=` fetch for the tail instead of re-downloading whole
   * conversations (which for a long conversation is megabytes).
   */
  markAllStale(): void {
    const dirty: string[] = [];
    for (const rec of this.hot.values()) {
      if (!rec.needsRefresh) {
        rec.needsRefresh = true;
        rec.updatedAt = Date.now();
        dirty.push(rec.conversation_id);
        const set = this.listenersById.get(rec.conversation_id);
        if (set) for (const cb of set) cb();
      }
    }
    // A hydrate() in flight will install a record built from a disk row that
    // predates this reconnect, which would silently drop the flag. Tell it.
    for (const state of this.hydrating.values()) {
      state.staleWhileHydrating = true;
    }
    // Conversations not currently in the hot map are handled by hydrate():
    // a disk row that predates this reconnect is only reachable through
    // hydrate, which happens on focus, and focus always re-checks the tail
    // when the list's known-max is ahead of the cache.
    if (dirty.length > 0) {
      cacheDiag("info", "stale.reconnect", { conversations: dirty.length });
      for (const cb of this.allListeners) cb();
      for (const id of dirty) {
        this.queueWrite(id, () => this._patchMeta(id, { needs_refresh: true })).catch((err) =>
          cacheDiag("fail", "persist.mark_stale_failed", { error: String(err) }, id),
        );
      }
    }
  }

  // ── delete ─────────────────────────────────────────────────────────────────

  async delete(id: string): Promise<void> {
    this.hot.delete(id);
    this.transient.delete(id);
    this.hydrated.delete(id);
    this.notify(id);
    // Wait for any in-flight write-behind ops for this conversation to
    // settle before deleting, so a slow upsert can't race past us and
    // recreate rows after the delete, and put the delete itself on the same
    // per-conversation chain so anything queued while we waited runs before
    // it. A persister enqueued after this point still lands behind the delete
    // and recreates rows — unchanged from before the chain, and bounded the
    // same way: the caller stops streaming into a conversation it deletes.
    await this.settle();
    const p = this.queueWrite(id, async () => {
      const db = await this.db();
      const tx = db.transaction(["messages", "conversation_meta"], "readwrite");
      await tx.objectStore("messages").delete(convRange(id));
      await tx.objectStore("conversation_meta").delete(id);
      await tx.done;
    });
    p.catch(() => {});
    try {
      await p;
    } catch (err) {
      cacheDiag("fail", "delete.idb_failed", { conversation_id: id, error: String(err) }, id);
    }
  }

  // ── pruneStale ─────────────────────────────────────────────────────────────

  /**
   * Delete cached rows for conversations that are no longer in the active
   * set (i.e. the server's conversation list) and whose meta row hasn't
   * been touched in `olderThanMs`. Intended for archived/forgotten
   * conversations so the IDB cache doesn't grow without bound.
   *
   * `activeIds` is the set of conversation_ids currently known to the
   * server. Anything outside that set whose `updated_at < now - olderThanMs`
   * is dropped (both messages and meta).
   *
   * Returns the list of pruned conversation_ids.
   */
  async pruneStale(activeIds: Iterable<string>, olderThanMs: number): Promise<string[]> {
    if (!this.factory) return [];
    const active = new Set(activeIds);
    const cutoff = Date.now() - olderThanMs;
    let toPrune: string[];
    try {
      const db = await this.db();
      const metas = await db.getAll("conversation_meta");
      toPrune = metas
        .filter((m) => !active.has(m.conversation_id) && m.updated_at < cutoff)
        .map((m) => m.conversation_id);
    } catch (err) {
      cacheDiag("fail", "prune.scan_failed", { error: String(err) });
      return [];
    }
    const pruned: string[] = [];
    for (const id of toPrune) {
      try {
        // Settle any in-flight writes for this conv so we don't race a
        // concurrent upsert (e.g. a live stream event landing during prune).
        await this.settle();
        const db = await this.db();
        const tx = db.transaction(["messages", "conversation_meta"], "readwrite");
        // Re-read the meta row INSIDE the prune tx and verify it's still
        // stale. If a stream event upserted it after our scan, skip.
        const meta = await tx.objectStore("conversation_meta").get(id);
        if (!meta || meta.updated_at >= cutoff) {
          await tx.done;
          continue;
        }
        await tx.objectStore("messages").delete(convRange(id));
        await tx.objectStore("conversation_meta").delete(id);
        await tx.done;
        // Drop from hot map AFTER the tx commits so a racing
        // upsert that landed mid-delete can immediately repopulate.
        this.hot.delete(id);
        this.transient.delete(id);
        this.hydrated.delete(id);
        this.notify(id);
        pruned.push(id);
      } catch (err) {
        cacheDiag("fail", "prune.delete_failed", { conversation_id: id, error: String(err) }, id);
      }
    }
    return pruned;
  }

  // ── clear ──────────────────────────────────────────────────────────────────

  async clear(): Promise<void> {
    await this.settle();
    this.hot.clear();
    this.transient.clear();
    this.hydrated.clear();
    try {
      const db = await this.db();
      const tx = db.transaction(["messages", "conversation_meta", "keys_meta"], "readwrite");
      await tx.objectStore("messages").clear();
      await tx.objectStore("conversation_meta").clear();
      await tx.objectStore("keys_meta").clear();
      await tx.done;
    } catch (err) {
      cacheDiag("fail", "clear.idb_failed", { error: String(err) });
    }
    for (const cbs of this.listenersById.values()) {
      for (const cb of cbs) cb();
    }
    for (const cb of this.allListeners) cb();
  }

  /**
   * Tell the server to invalidate the cache session, drop our in-memory
   * key, and wipe IDB. Use on explicit logout / "clear local cache". The
   * next operation will fetch a fresh key and a fresh empty DB.
   *
   * Drains in-flight write-behind tasks BEFORE touching the key/cache so
   * we cannot leave behind rows that were encrypted under the old key but
   * land in IDB *after* the wipe (which would then be undecryptable
   * garbage that survives until the next rotation — they look fresh to
   * the next-key keys_meta and bypass the wipe-on-mismatch path).
   */
  async wipeAndRotateKey(): Promise<void> {
    await this.settle();
    try {
      await this.keyHolder.clear();
    } catch (err) {
      // Server clear() failed (e.g. 500 / network). Don't blow away IDB
      // locally: the user thinks the cache is wiped, but the next
      // GET /api/cache-key would still hand back the old key_id and
      // our wipe-on-mismatch path wouldn't fire, leaving a tab that
      // *thinks* it rotated but didn't. Surface the failure to the
      // caller (CommandPalette currently reloads on success only via
      // its .then; this rejects the promise so .then is skipped).
      console.warn("messageStore.wipeAndRotateKey: clear server session failed:", err);
      throw err;
    }
    await this.clear();
    // Force the next db() call to re-open and pick up the new key_id.
    if (this.dbPromise) {
      try {
        (await this.dbPromise).close();
      } catch {
        /* ignore */
      }
      this.dbPromise = null;
    }
    // Tell sibling tabs to drop their cached keys + db handles.
    this.rotateChannel?.postMessage({ type: "rotated" } satisfies RotateMsg);
  }

  // ── Subscribe ──────────────────────────────────────────────────────────────

  subscribe(id: string, cb: Listener): () => void {
    let set = this.listenersById.get(id);
    if (!set) {
      set = new Set();
      this.listenersById.set(id, set);
    }
    set.add(cb);
    return () => {
      set!.delete(cb);
      if (set!.size === 0) this.listenersById.delete(id);
    };
  }

  subscribeTransient(id: string, cb: Listener): () => void {
    let set = this.transientListenersById.get(id);
    if (!set) {
      set = new Set();
      this.transientListenersById.set(id, set);
    }
    set.add(cb);
    return () => {
      set!.delete(cb);
      if (set!.size === 0) this.transientListenersById.delete(id);
    };
  }

  subscribeAll(cb: Listener): () => void {
    this.allListeners.add(cb);
    return () => {
      this.allListeners.delete(cb);
    };
  }

  // ── Notify helpers ─────────────────────────────────────────────────────────

  private notify(id: string): void {
    perfCount("store.notify");
    const set = this.listenersById.get(id);
    if (set) for (const cb of set) cb();
    for (const cb of this.allListeners) cb();
  }

  private notifyTransient(id: string): void {
    perfCount("store.notifyTransient");
    const set = this.transientListenersById.get(id);
    if (set) for (const cb of set) cb();
  }
}

export const messageStore = new MessageStore();
