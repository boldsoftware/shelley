// What a view (services/mcpAppHost.ts) asks to send as the user goes into the
// conversation's text box (MessageInput) instead: the user reads it, edits it
// or clears it, and their Send is their OK. A conversation holds one; a newer
// one takes its place.
import { shallowReactive } from "vue";

export interface ViewDraft {
  id: number;
  source: string;
  text: string;
  /** The text box's text before and after the fill, set once it has taken
   *  the view's text. */
  filled?: { before: string; after: string };
  /** Set while the user's message is on its way, so emptying the text box
   *  as it goes doesn't count as clearing it. */
  sending: boolean;
  /** Whether the user sent it, once settled. */
  sent?: boolean;
  owner: object;
  /** Tells the view whether the user sent it, and lets the draft go. */
  settle(sent: boolean): void;
}

const drafts = shallowReactive(new Map<string, ViewDraft>());
const composers = new Map<string, Set<() => boolean>>();
let lastID = 0;

/** Marks the conversation's text box as showing, until the returned function
 *  is called. While busy() holds, views wait their turn. */
export function openComposer(conversationId: string, busy: () => boolean): () => void {
  const open = composers.get(conversationId) ?? new Set();
  composers.set(conversationId, open);
  open.add(busy);
  return () => {
    open.delete(busy);
    if (!open.size && composers.get(conversationId) === open) composers.delete(conversationId);
  };
}

/** Puts text in the conversation's text box; resolves with whether the user
 *  sent it. */
export function offerViewDraft(
  conversationId: string,
  owner: object,
  source: string,
  text: string,
): Promise<boolean> {
  const open = composers.get(conversationId);
  if (!open) return Promise.reject(new Error("The conversation's text box isn't showing."));
  const previous = drafts.get(conversationId);
  if (previous?.sending || [...open].some((busy) => busy())) {
    return Promise.reject(new Error("The text box is busy; try again later."));
  }
  previous?.settle(false);
  return new Promise((resolve) => {
    const draft: ViewDraft = {
      id: ++lastID,
      source,
      text,
      sending: false,
      owner,
      settle(sent) {
        if (draft.sent !== undefined) return;
        draft.sent = sent;
        if (drafts.get(conversationId) === draft) drafts.delete(conversationId);
        resolve(sent);
      },
    };
    drafts.set(conversationId, draft);
  });
}

/** Whether the text box takes text as a command (! for the shell, / for
 *  Shelley's), not as a message. */
export function isCommand(text: string): boolean {
  return /^[!/]/.test(text.trim());
}

export function viewDraftFor(conversationId: string | null | undefined): ViewDraft | undefined {
  return conversationId ? drafts.get(conversationId) : undefined;
}

/** Lets go of the owner's drafts, as not sent; the text box takes back what
 *  the user left untouched. */
export function withdrawViewDrafts(owner: object): void {
  for (const draft of [...drafts.values()]) {
    if (draft.owner === owner) draft.settle(false);
  }
}
