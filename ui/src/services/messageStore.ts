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

// Cross-tab notification channel for key rotation. When one tab runs
// wipeAndRotateKey() the others must drop their cached CryptoKey and
// reopen the DB so they don't keep writing old-key ciphertext into a
// store that's been re-keyed under them. Optional chaining for
// environments without BroadcastChannel (older Safari / tests).
const ROTATE_CHANNEL = "shelley-cache-rotate";
type RotateMsg = { type: "rotated" };

const DEFAULT_DB_NAME = "shelley-messages";
const DB_VERSION = 4;

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
  updatedAt: number;
}

// ─── Transient (non-persisted) state ─────────────────────────────────────────

export interface TransientState {
  toolProgress: Record<string, ToolProgress>;
  streamingText: string;
  agentWorking: boolean;
}

function emptyTransient(): TransientState {
  return { toolProgress: {}, streamingText: "", agentWorking: false };
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
    updatedAt: Date.now(),
  };
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
}

export class MessageStore {
  private readonly dbName: string;
  private readonly factory: IDBFactory | undefined;
  private readonly keyHolder: CacheKeyHolder;
  private dbPromise: Promise<IDBPDatabase<ShelleyDB>> | null = null;
  private hot = new Map<string, ConversationCacheRecord>();
  private transient = new Map<string, TransientState>();
  private hydrated = new Set<string>();
  private listenersById = new Map<string, Set<Listener>>();
  private transientListenersById = new Map<string, Set<Listener>>();
  private allListeners = new Set<Listener>();
  /** Pending write-behind operations. `settle()` awaits these. */
  private inflight = new Set<Promise<unknown>>();
  /** Cross-tab rotation channel; null when BroadcastChannel is unavailable. */
  private rotateChannel: BroadcastChannel | null = null;

  constructor(opts: MessageStoreOptions = {}) {
    this.dbName = opts.dbName ?? DEFAULT_DB_NAME;
    this.factory = opts.factory ?? (typeof indexedDB !== "undefined" ? indexedDB : undefined);
    this.keyHolder = opts.keyHolder ?? new CacheKeyHolder(new HttpCacheKeyFetcher());
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
      const tx = db.transaction(["keys_meta", "messages", "conversation_meta"], "readwrite");
      const km = tx.objectStore("keys_meta");
      const existing = await km.get("current");
      if (!existing) {
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
      } else if (existing.key_id !== material.keyId) {
        // Server rotated; old rows are useless. Wipe.
        await tx.objectStore("messages").clear();
        await tx.objectStore("conversation_meta").clear();
        await km.put({ id: "current", key_id: material.keyId });
      }
      await tx.done;
    } catch (err) {
      db.close();
      throw err;
    }
    return db;
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
    };
    const globalFactory = typeof indexedDB !== "undefined" ? indexedDB : undefined;
    if (this.factory === globalFactory) {
      return openDB<ShelleyDB>(this.dbName, DB_VERSION, callbacks);
    }
    // Test path: a custom factory was injected. `idb` reads
    // `globalThis.indexedDB` directly, so temporarily swap it.
    const g = globalThis as { indexedDB?: IDBFactory };
    const prev = g.indexedDB;
    g.indexedDB = this.factory;
    try {
      return await openDB<ShelleyDB>(this.dbName, DB_VERSION, callbacks);
    } finally {
      g.indexedDB = prev;
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
      console.warn("messageStore: undecryptable message row", row.message_id, err);
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
      console.warn("messageStore: undecryptable meta row", row.conversation_id, err);
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

  /** Load a conversation from IDB into the hot cache if not already loaded. */
  async hydrate(id: string): Promise<ConversationCacheRecord | null> {
    if (this.hydrated.has(id)) {
      return this.hot.get(id) ?? null;
    }
    let rec: ConversationCacheRecord | null = null;
    try {
      const material = await this.getKey();
      if (!material) {
        this.hydrated.add(id);
        return null;
      }
      const db = await this.db();
      const meta = await db.get("conversation_meta", id);
      if (meta) {
        const payload = await this.decryptMetaRow(material.key, meta);
        if (payload) {
          // getAll on the compound key range returns rows in ascending
          // (conv, seq) order — no JS sort needed.
          const rows = await db.getAll("messages", convRange(id));
          const decrypted: Message[] = [];
          for (const r of rows) {
            const m = await this.decryptMessageRow(material.key, r);
            if (m) decrypted.push(m);
          }
          const minSeq = decrypted.length > 0 ? decrypted[0].sequence_id : 0;
          const maxSeq = decrypted.length > 0 ? decrypted[decrypted.length - 1].sequence_id : -1;
          rec = {
            conversation_id: id,
            messages: decrypted,
            conversation: payload.conversation,
            contextWindowSize: payload.context_window_size,
            minSequenceId: minSeq,
            maxSequenceId: maxSeq,
            maxSequenceIdKnown: meta.max_sequence_id_known,
            hasFullHistory: meta.has_full_history,
            updatedAt: meta.updated_at,
          };
        }
      }
    } catch (err) {
      console.warn("messageStore.hydrate: IDB read failed:", err);
    }
    this.hydrated.add(id);
    if (rec) {
      this.hot.set(id, rec);
      this.notify(id);
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

  // ── upsertMessages ─────────────────────────────────────────────────────────

  /** Merge a batch of messages into the per-conv cache (streaming upsert). */
  upsertMessages(id: string, incoming: Message[]): void {
    if (incoming.length === 0) return;
    const rec = this.hot.get(id) ?? emptyRecord(id);
    const byMsgId = new Map<string, Message>();
    for (const m of rec.messages) byMsgId.set(m.message_id, m);
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
    this.hydrated.add(id);
    this.notify(id);

    // Snapshot what to persist; do not rely on hot record mutating between now
    // and when the tx runs.
    const snapshotIncoming = incoming.slice();
    const snapshotKnown = rec.maxSequenceIdKnown;
    const snapshotConv = rec.conversation;
    const snapshotCtx = rec.contextWindowSize;
    this.track(
      this._persistUpsert(id, snapshotIncoming, snapshotKnown, snapshotConv, snapshotCtx),
    ).catch((err) => console.warn("messageStore.upsertMessages: persist failed:", err));
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
    for (const m of incoming) {
      encRows.push(await this.encryptMessageRow(material.key, m));
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
    let maxLocal = existing?.max_sequence_id_local ?? -1;
    const idIdx = msgs.index("by_message_id");
    for (let i = 0; i < encRows.length; i++) {
      const row = encRows[i];
      const m = incoming[i];
      const priorKey = await idIdx.getKey(m.message_id);
      if (priorKey && (priorKey[0] !== m.conversation_id || priorKey[1] !== m.sequence_id)) {
        await msgs.delete(priorKey);
      }
      await msgs.put(row);
      if (m.sequence_id > maxLocal) maxLocal = m.sequence_id;
    }
    const metaRow: ConvMetaRow = {
      conversation_id: id,
      updated_at: Date.now(),
      max_sequence_id_known: Math.max(
        existing?.max_sequence_id_known ?? 0,
        knownHint,
        maxLocal < 0 ? 0 : maxLocal,
      ),
      max_sequence_id_local: maxLocal,
      has_full_history: existing?.has_full_history ?? false,
      iv,
      ct,
    };
    await metaStore.put(metaRow);
    await tx.done;
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
    const rec: ConversationCacheRecord = {
      conversation_id: id,
      messages,
      conversation: response.conversation ?? existing?.conversation ?? null,
      contextWindowSize: response.context_window_size ?? existing?.contextWindowSize ?? 0,
      minSequenceId: minSeq,
      maxSequenceId: maxSeq,
      maxSequenceIdKnown: knownAfter,
      hasFullHistory: true,
      updatedAt: Date.now(),
    };
    this.hot.set(id, rec);
    this.hydrated.add(id);
    this.notify(id);

    this.track(this._persistFullHistory(id, rec)).catch((err) =>
      console.warn("messageStore.applyFullHistory: persist failed:", err),
    );
  }

  private async _persistFullHistory(id: string, rec: ConversationCacheRecord): Promise<void> {
    const material = await this.getKey();
    if (!material) return;
    // Encrypt all message rows + the meta payload OUTSIDE the IDB tx.
    const encMsgs: MessageRow[] = [];
    for (const m of rec.messages) {
      encMsgs.push(await this.encryptMessageRow(material.key, m));
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
    for (const r of encMsgs) {
      await msgs.put(r);
    }
    const row: ConvMetaRow = {
      conversation_id: id,
      updated_at: Date.now(),
      max_sequence_id_known: Math.max(existing?.max_sequence_id_known ?? 0, rec.maxSequenceIdKnown),
      // Ratchet against any concurrent writer that pushed local higher.
      max_sequence_id_local: Math.max(existing?.max_sequence_id_local ?? -1, rec.maxSequenceId),
      has_full_history: true,
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
    this.hydrated.add(id);
    this.notify(id);
    this.track(this._patchMeta(id, { conversation: conv })).catch((err) =>
      console.warn("messageStore.setConversation: persist failed:", err),
    );
  }

  // ── setContextWindowSize ───────────────────────────────────────────────────

  setContextWindowSize(id: string, size: number): void {
    const rec = this.hot.get(id) ?? emptyRecord(id);
    if (rec.contextWindowSize === size) return;
    rec.contextWindowSize = size;
    rec.updatedAt = Date.now();
    this.hot.set(id, rec);
    this.hydrated.add(id);
    this.notify(id);
    this.track(this._patchMeta(id, { context_window_size: size })).catch((err) =>
      console.warn("messageStore.setContextWindowSize: persist failed:", err),
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
    this.hydrated.add(id);
    this.notify(id);
    this.track(this._patchMeta(id, { max_sequence_id_known: maxSeq })).catch((err) =>
      console.warn("messageStore.setMaxSequenceIdKnown: persist failed:", err),
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

  appendStreamDelta(id: string, text: string): void {
    if (!text) return;
    const t = this.getTransient(id);
    t.streamingText = t.streamingText + text;
    this.notifyTransient(id);
  }

  resetStreamingText(id: string): void {
    const t = this.getTransient(id);
    if (!t.streamingText) return;
    t.streamingText = "";
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
    // toolProgress and streamingText, on the other hand, are stream-only
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
   * Mark every cached conversation as stale (hasFullHistory=false).
   * Called after a global-stream reconnect to ensure the next focus
   * triggers a REST backfill. Messages on disk are preserved.
   */
  markAllStale(): void {
    const dirty: string[] = [];
    for (const rec of this.hot.values()) {
      if (rec.hasFullHistory) {
        rec.hasFullHistory = false;
        rec.updatedAt = Date.now();
        dirty.push(rec.conversation_id);
        const set = this.listenersById.get(rec.conversation_id);
        if (set) for (const cb of set) cb();
      }
    }
    if (dirty.length > 0) {
      for (const cb of this.allListeners) cb();
      for (const id of dirty) {
        this.track(this._patchMeta(id, { has_full_history: false })).catch((err) =>
          console.warn("messageStore.markAllStale: persist failed:", err),
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
    // recreate rows after the delete.
    await this.settle();
    const p = (async () => {
      const db = await this.db();
      const tx = db.transaction(["messages", "conversation_meta"], "readwrite");
      await tx.objectStore("messages").delete(convRange(id));
      await tx.objectStore("conversation_meta").delete(id);
      await tx.done;
    })();
    this.track(p).catch(() => {});
    try {
      await p;
    } catch (err) {
      console.warn("messageStore.delete: IDB delete failed:", err);
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
      console.warn("messageStore.pruneStale: scan failed:", err);
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
        console.warn("messageStore.pruneStale: delete failed for", id, err);
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
      console.warn("messageStore.clear: IDB clear failed:", err);
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
    const set = this.listenersById.get(id);
    if (set) for (const cb of set) cb();
    for (const cb of this.allListeners) cb();
  }

  private notifyTransient(id: string): void {
    const set = this.transientListenersById.get(id);
    if (set) for (const cb of set) cb();
  }
}

export const messageStore = new MessageStore();
