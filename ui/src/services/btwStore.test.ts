import assert from "node:assert/strict";
import type {
  BtwExchange,
  BtwReaderDescriptor,
  Conversation,
  Message,
  StreamResponse,
} from "../types";
import {
  agent,
  childConversation,
  idleTransient,
  parentID,
  readerDescriptor,
  user,
} from "../test/btwFixtures";
import { ApiError } from "./api";
import type { ConversationCacheRecord } from "./messageStore";
import { BtwStore } from "./btwStore";

const descriptor = readerDescriptor();
const conversation = childConversation();
const question = user(1, "question");
const answer = agent(2, "answer");

function record(conversation: Conversation, messages: Message[]): ConversationCacheRecord {
  return {
    conversation_id: conversation.conversation_id,
    messages,
    conversation,
    contextWindowSize: 0,
    minSequenceId: messages[0]?.sequence_id ?? 0,
    maxSequenceId: messages.at(-1)?.sequence_id ?? -1,
    maxSequenceIdKnown: messages.at(-1)?.sequence_id ?? 0,
    hasFullHistory: true,
    needsRefresh: false,
    updatedAt: 0,
  };
}

function childStore() {
  const records = new Map<string, ConversationCacheRecord>();
  const persistent = new Map<string, () => void>();
  const transient = new Map<string, () => void>();
  let unsubscribes = 0;
  return {
    records,
    persistent,
    transient,
    get unsubscribes() {
      return unsubscribes;
    },
    store: {
      peek(id: string) {
        return records.get(id) ?? null;
      },
      getTransient() {
        return idleTransient;
      },
      applyFullHistory(id: string, response: StreamResponse) {
        records.set(
          id,
          record((response.conversation ?? childConversation(id)) as Conversation, [
            ...((response.messages ?? []) as Message[]),
          ]),
        );
        persistent.get(id)?.();
      },
      subscribe(id: string, listener: () => void) {
        persistent.set(id, listener);
        return () => {
          persistent.delete(id);
          unsubscribes++;
        };
      },
      subscribeTransient(id: string, listener: () => void) {
        transient.set(id, listener);
        return () => {
          transient.delete(id);
          unsubscribes++;
        };
      },
    },
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => (resolve = done));
  return { promise, resolve };
}

{
  const children = childStore();
  const store = new BtwStore(
    {
      listBtwReaders: async () => [],
      dismissBtwExchange: async () => {},
      getConversationWithProgress: async () => ({ conversation, messages: [] }),
    },
    children.store,
    null,
  );
  for (const [childID, createdAt] of [
    ["old", "2026-08-31T12:00:00Z"],
    ["alpha", "2026-08-31T12:01:00Z"],
    ["beta", "2026-08-31T12:01:00Z"],
  ] as const) {
    const child = childConversation(childID, createdAt);
    children.records.set(
      childID,
      record(child, [
        user(1, "question", { conversationID: childID }),
        agent(2, "answer", { conversationID: childID }),
      ]),
    );
    store.upsert(readerDescriptor(childID));
  }
  assert.deepEqual(
    store.list(parentID).map((exchange) => exchange.exchange_id),
    ["beta", "alpha", "old"],
    "metadata sorts newest-first with a stable ID tie-break",
  );
}

{
  const children = childStore();
  const first = deferred<BtwReaderDescriptor[]>();
  let lists = 0;
  const store = new BtwStore(
    {
      listBtwReaders: async () => (++lists === 1 ? first.promise : [descriptor]),
      dismissBtwExchange: async () => {},
      getConversationWithProgress: async () => ({ conversation, messages: [question, answer] }),
    },
    children.store,
    null,
  );
  const hydration = store.hydrate(parentID);
  assert.equal(hydration, store.hydrate(parentID), "metadata hydration is single-flight");
  store.upsert(descriptor);
  first.resolve([]);
  await hydration;
  assert.equal(store.list(parentID).length, 1, "a stale list cannot erase an accepted reader");
  assert.equal(lists, 2, "a changed revision is re-listed before commit");
  let notifications = 0;
  store.subscribe(parentID, () => notifications++);
  children.persistent.get("child")?.();
  children.transient.get("child")?.();
  assert.equal(notifications, 2, "persistent and transient child updates notify the parent");
}

for (const scenario of [
  {
    name: "an attached removal is not resurrected",
    before: (store: BtwStore) => store.upsert(descriptor),
    remove: (store: BtwStore) => store.removeReader("child"),
    unsubscribes: 2,
  },
  {
    name: "a pre-attachment clear is not resurrected",
    before: () => {},
    remove: (store: BtwStore) => store.clear("child", parentID),
    unsubscribes: 0,
  },
]) {
  const children = childStore();
  const first = deferred<BtwReaderDescriptor[]>();
  let lists = 0;
  const store = new BtwStore(
    {
      listBtwReaders: async () => (++lists === 1 ? first.promise : []),
      dismissBtwExchange: async () => {},
      getConversationWithProgress: async () => ({ conversation, messages: [question, answer] }),
    },
    children.store,
    null,
  );
  scenario.before(store);
  const hydration = store.hydrate(parentID);
  scenario.remove(store);
  first.resolve([descriptor]);
  await hydration;
  assert.deepEqual(
    [store.list(parentID).length, lists, children.unsubscribes],
    [0, 2, scenario.unsubscribes],
    scenario.name,
  );
}

{
  const children = childStore();
  const store = new BtwStore(
    {
      listBtwReaders: async () => [descriptor],
      dismissBtwExchange: async () => {},
      getConversationWithProgress: async () => {
        throw new ApiError("gone", 404);
      },
    },
    children.store,
    null,
  );
  await store.hydrate(parentID);
  assert.equal(store.list(parentID).length, 0, "a child hydrate 404 removes its descriptor");
}

{
  const children = childStore();
  children.records.set("child", record(conversation, [question, answer]));
  const dismissed: Array<[string, string]> = [];
  const store = new BtwStore(
    {
      listBtwReaders: async () => [descriptor],
      dismissBtwExchange: async (parentID, childID) => {
        dismissed.push([parentID, childID]);
      },
      getConversationWithProgress: async () => ({ conversation, messages: [question, answer] }),
    },
    children.store,
    null,
  );
  store.upsert(descriptor);
  const cachedChild = children.records.get("child");
  let notifications = 0;
  store.subscribe(parentID, () => notifications++);

  await store.dismiss(store.list(parentID)[0]);

  assert.deepEqual(
    dismissed,
    [[parentID, "child"]],
    "dismiss uses the reader's parent and child IDs",
  );
  assert.equal(store.list(parentID).length, 0, "dismiss removes the inline reader");
  assert.equal(children.unsubscribes, 2, "dismiss detaches child subscriptions");
  assert.equal(notifications, 1, "dismiss notifies inline readers");
  assert.equal(
    children.records.get("child"),
    cachedChild,
    "dismiss retains the child conversation cache",
  );
}

const oldSummary = user(1, "summary prompt", { userData: { btw_turn_kind: "summary" } });
const oldAnswer = agent(2, "old summary");
const newSummary = user(3, "summary prompt", { userData: { btw_turn_kind: "summary" } });
const newAnswer = agent(4, "new summary");
const priorExchange: BtwExchange = {
  exchange_id: "child",
  parent_conversation_id: parentID,
  status: "completed",
  retryable: false,
  parent_pointer: descriptor.parent_pointer,
  created_at: conversation.created_at,
  turns: [
    {
      id: "m1",
      question: "Summary for main chat",
      answer: "old",
      status: "completed",
      kind: "summary",
    },
  ],
};
const acceptedAPI = {
  listBtwReaders: async () => [descriptor],
  dismissBtwExchange: async () => {},
  getConversationWithProgress: async () => ({
    conversation,
    messages: [oldSummary, oldAnswer, newSummary, newAnswer],
  }),
};

for (const reload of [false, true]) {
  const values = new Map<string, string>();
  const storage = {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
  };
  const requested = new BtwStore(acceptedAPI, childStore().store, storage);
  requested.requestSummary(priorExchange);
  if (!reload) requested.resolveSummary(priorExchange);
  const claimant = reload ? new BtwStore(acceptedAPI, childStore().store, storage) : requested;
  await claimant.hydrate(parentID);
  const exchange = claimant.list(parentID)[0];
  assert.equal(
    claimant.claimSummary(exchange)?.id,
    "m3",
    reload
      ? "reload recovers the accepted summary"
      : "response loss is reconciled by child hydration",
  );
  assert.equal(claimant.claimSummary(exchange), null, "a summary is claimed exactly once");
}

{
  const values = new Map<string, string>();
  const storage = {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
  };
  const interloperSummary = user(3, "summary prompt", {
    userData: { btw_turn_kind: "summary" },
  });
  const interloperAnswer = agent(4, "interloper summary");
  const acceptedSummary = user(5, "summary prompt", {
    userData: { btw_summary_id: "s5", btw_turn_kind: "summary" },
  });
  const acceptedAnswer = agent(6, "accepted summary");
  const exact = new BtwStore(
    {
      listBtwReaders: async () => [descriptor],
      dismissBtwExchange: async () => {},
      getConversationWithProgress: async () => ({
        conversation,
        messages: [
          oldSummary,
          oldAnswer,
          interloperSummary,
          interloperAnswer,
          acceptedSummary,
          acceptedAnswer,
        ],
      }),
    },
    childStore().store,
    storage,
  );
  exact.requestSummary(priorExchange);
  await exact.hydrate(parentID);
  const exchange = exact.list(parentID)[0];
  assert.equal(
    exact.claimSummary(exchange),
    null,
    "positional recovery waits while the summary receipt is still in flight",
  );
  exact.resolveSummary(priorExchange, "s5");
  assert.equal(
    exact.claimSummary(exchange)?.id,
    "m5",
    "the server-returned summary ID wins over positional matching",
  );
  assert.equal(exact.claimSummary(exchange), null, "the exact summary is claimed once");
}

console.log("✓ BTW store is ordered, race-safe, subscribed, and summary intent is durable");
