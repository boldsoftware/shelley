import type { BtwExchange, BtwReaderDescriptor, BtwTurn } from "../types";
import { ApiError, api } from "./api";
import { messageStore } from "./messageStore";
import { projectBtwReader } from "./btwProjector";
import * as summaryIntent from "./btwSummaryIntent";

type API = Pick<
  typeof api,
  "listBtwReaders" | "dismissBtwExchange" | "getConversationWithProgress"
>;
type Children = Pick<
  typeof messageStore,
  "peek" | "getTransient" | "applyFullHistory" | "subscribe" | "subscribeTransient"
>;
interface ParentState {
  descriptors: Map<string, BtwReaderDescriptor>;
  listeners: Set<() => void>;
  revision: number;
  hydration?: Promise<void>;
  disposed: boolean;
}

interface ChildSubscription {
  parentID: string;
  unsubscribe: () => void;
}

export class BtwStore {
  private parents = new Map<string, ParentState>();
  private childSubscriptions = new Map<string, ChildSubscription>();
  private pendingSummaryResponses = new Set<string>();

  constructor(
    private readonly btwAPI: API = api,
    private readonly children: Children = messageStore,
    private readonly storage: summaryIntent.SummaryStorage | null = typeof localStorage ===
    "undefined"
      ? null
      : localStorage,
  ) {}

  list(parentID: string): BtwExchange[] {
    return [...this.parent(parentID).descriptors.values()]
      .map((descriptor) => {
        const childID = descriptor.conversation_id;
        return projectBtwReader(
          descriptor,
          this.children.peek(childID),
          this.children.getTransient(childID),
        );
      })
      .filter((exchange) => exchange.turns.length)
      .sort(
        (a, b) =>
          Date.parse(b.created_at) - Date.parse(a.created_at) ||
          b.exchange_id.localeCompare(a.exchange_id),
      );
  }

  subscribe(parentID: string, listener: () => void): () => void {
    const parent = this.parent(parentID);
    parent.listeners.add(listener);
    return () => parent.listeners.delete(listener);
  }

  upsert(descriptor: BtwReaderDescriptor): void {
    const parent = this.parent(descriptor.parent_conversation_id);
    parent.descriptors.set(descriptor.conversation_id, descriptor);
    parent.revision++;
    this.attach(descriptor);
    this.emit(descriptor.parent_conversation_id);
  }

  hydrate(parentID: string): Promise<void> {
    const parent = this.parent(parentID);
    parent.hydration ??= this.sync(parentID, parent).finally(() => {
      parent.hydration = undefined;
    });
    return parent.hydration;
  }

  private async sync(parentID: string, parent: ParentState): Promise<void> {
    let readers: BtwReaderDescriptor[];
    let revision: number;
    do {
      revision = parent.revision;
      readers = await this.btwAPI.listBtwReaders(parentID);
    } while (!parent.disposed && revision !== parent.revision);
    if (parent.disposed) return;

    const next = new Map(readers.map((reader) => [reader.conversation_id, reader]));
    for (const childID of parent.descriptors.keys()) {
      if (!next.has(childID)) this.detach(childID);
    }
    parent.descriptors = next;
    readers.forEach((reader) => this.attach(reader));
    this.emit(parentID);
    await Promise.all(readers.map((reader) => this.refreshChild(reader.conversation_id)));
  }

  async refreshChild(childID: string): Promise<void> {
    try {
      const snapshot = await this.btwAPI.getConversationWithProgress(childID);
      this.children.applyFullHistory(childID, snapshot);
    } catch (error) {
      if (error instanceof ApiError && error.status === 404) this.removeReader(childID);
      else throw error;
    }
  }

  removeReader(childID: string, parentHint?: string): void {
    this.pendingSummaryResponses.delete(childID);
    const parentID = this.childSubscriptions.get(childID)?.parentID ?? parentHint;
    if (!parentID) return;
    const parent = this.parents.get(parentID);
    if (!parent) {
      this.detach(childID);
      return;
    }
    parent.descriptors.delete(childID);
    parent.revision++;
    this.detach(childID);
    this.emit(parentID);
  }

  async dismiss(exchange: BtwExchange): Promise<void> {
    await this.btwAPI.dismissBtwExchange(exchange.parent_conversation_id, exchange.exchange_id);
    this.removeReader(exchange.exchange_id, exchange.parent_conversation_id);
  }

  clear(conversationID: string, parentHint?: string): void {
    this.removeReader(conversationID, parentHint);
    const parent = this.parents.get(conversationID);
    if (parent) parent.disposed = true;
    for (const childID of parent?.descriptors.keys() ?? []) {
      this.pendingSummaryResponses.delete(childID);
      this.detach(childID);
    }
    this.parents.delete(conversationID);
    summaryIntent.clearSummaryIntents(this.storage, conversationID);
  }

  requestSummary(exchange: BtwExchange): void {
    summaryIntent.requestSummaryIntent(this.storage, exchange);
    this.pendingSummaryResponses.add(exchange.exchange_id);
  }

  resolveSummary(exchange: BtwExchange, summaryID?: string): void {
    if (summaryID) summaryIntent.resolveSummaryIntent(this.storage, exchange, summaryID);
    this.pendingSummaryResponses.delete(exchange.exchange_id);
  }

  claimSummary(exchange: BtwExchange): BtwTurn | null {
    if (this.pendingSummaryResponses.has(exchange.exchange_id)) return null;
    return summaryIntent.claimSummaryIntent(this.storage, exchange);
  }

  private attach(descriptor: BtwReaderDescriptor): void {
    const childID = descriptor.conversation_id;
    const parentID = descriptor.parent_conversation_id;
    const attached = this.childSubscriptions.get(childID);
    if (attached?.parentID === parentID) return;
    if (attached) this.detach(childID);

    const notify = () => this.emit(parentID);
    const unsubscribeMessages = this.children.subscribe(childID, notify);
    const unsubscribeTransient = this.children.subscribeTransient(childID, notify);
    this.childSubscriptions.set(childID, {
      parentID,
      unsubscribe: () => {
        unsubscribeMessages();
        unsubscribeTransient();
      },
    });
  }

  private detach(childID: string): void {
    this.childSubscriptions.get(childID)?.unsubscribe();
    this.childSubscriptions.delete(childID);
  }

  private parent(id: string): ParentState {
    const parent = this.parents.get(id) ?? {
      descriptors: new Map(),
      listeners: new Set(),
      revision: 0,
      disposed: false,
    };
    this.parents.set(id, parent);
    return parent;
  }

  private emit(id: string): void {
    for (const listener of this.parents.get(id)?.listeners ?? []) listener();
  }
}
export const btwStore = new BtwStore();
