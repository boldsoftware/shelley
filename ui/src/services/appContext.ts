// What views asked the model to know with the user's next message (MCP Apps
// ui/update-model-context). A view's update replaces its previous one; the
// next message to the conversation takes them all.

export interface AppContext {
  /** Names the view to the model. */
  source: string;
  text: string;
  /** What shows the view: an mcpapp message's ID or a tool call's. The
   *  server keeps context from temporary views out of other messages. */
  view?: string;
}

/** What a send took: each view's context, and which update it was. */
export type TakenAppContext = Map<string, { context: AppContext; update: number }>;

/** Conversation ID -> view key -> context. */
const pending = new Map<string, TakenAppContext>();
/** Conversation ID -> view key -> its last update, kept past sends. */
const updates = new Map<string, Map<string, number>>();
let lastUpdate = 0;

/** Sets, or with null clears, what a view wants the model to know. */
export function setAppContext(conversationId: string, view: string, context: AppContext | null) {
  const update = ++lastUpdate;
  const viewUpdates = updates.get(conversationId) ?? new Map<string, number>();
  updates.set(conversationId, viewUpdates.set(view, update));
  const views = pending.get(conversationId) ?? new Map();
  if (context) views.set(view, { context, update });
  else views.delete(view);
  if (views.size) pending.set(conversationId, views);
  else pending.delete(conversationId);
}

/** Takes the conversation's pending context, to send with a message. */
export function takeAppContext(conversationId: string): TakenAppContext {
  const views = pending.get(conversationId) ?? new Map();
  pending.delete(conversationId);
  return views;
}

/** Puts back context a send took but didn't carry, unless its view has
 *  updated or cleared it since. */
export function restoreAppContext(conversationId: string, taken: TakenAppContext) {
  const views = pending.get(conversationId) ?? new Map();
  for (const [view, entry] of taken) {
    if (updates.get(conversationId)?.get(view) === entry.update) views.set(view, entry);
  }
  if (views.size) pending.set(conversationId, views);
}
