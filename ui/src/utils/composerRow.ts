import type { InjectionKey, Ref } from "vue";
import type { ConversationWithState } from "../types";

// The conversation this tab is composing (a new conversation or a draft) is
// shown in the drawer ahead of the server: from the moment the new-conversation
// view opens, before the draft autosave has created a row, and through the
// send, until the agent's reply previews it. The server's list then takes over
// without the row blinking out, moving, or going blank.

/** Drawer id of the composer's row while the server has yet to assign one.
 * Never a conversation id, so the row it marks has nothing to open or edit. */
export const COMPOSER_ROW_ID = "composer";

export interface ComposerRow {
  /** Server conversation id, once the draft or first message created one. */
  id: string | null;
  cwd: string | null;
  startedAt: string;
  /** The submitted message, which previews the row until the agent replies. */
  sent: string | null;
  /** The list has reported the id; if the row goes from it (deleted,
   * archived), so does the composer's. */
  listed: boolean;
}

/** Live composer text for the row being drafted; injected by drawer rows so a
 * keystroke re-renders only that row. */
export interface ComposerPreview {
  rowId: Readonly<Ref<string | null>>;
  text: Readonly<Ref<string>>;
  /** The placeholder can be deleted, leaving it for another row: there is
   * one to open, and nothing typed is saved or about to be. */
  leavable: Readonly<Ref<boolean>>;
}
export const ComposerPreviewKey: InjectionKey<ComposerPreview> = Symbol("composerPreview");

export function composerRowId(row: ComposerRow): string {
  return row.id ?? COMPOSER_ROW_ID;
}

/** `creating` lists the conversation ids known when a request creating the
 * composer's conversation went out, while one is pending. The list can report
 * the new row before the response names it, and the composer row already
 * stands in for it, so rows new since then are hidden until the id arrives. */
export function withComposerRow(
  list: ConversationWithState[],
  row: ComposerRow | null,
  creating: ReadonlySet<string> | null,
): ConversationWithState[] {
  if (!row) return list;
  const id = composerRowId(row);
  const known = row.id === null ? creating : null;
  let found = false;
  const out: ConversationWithState[] = [];
  for (const conversation of list) {
    if (conversation.conversation_id === id) {
      found = true;
      out.push(row.sent === null ? conversation : sentView(conversation, row.sent));
    } else if (
      known &&
      !known.has(conversation.conversation_id) &&
      !conversation.parent_conversation_id
    ) {
      continue;
    } else {
      out.push(conversation);
    }
  }
  if (found || row.listed) return out;
  return [placeholder(id, row, list), ...out];
}

// The server promotes the draft and previews the agent's reply a moment after
// the send; until then, show the row as sent with the user's message.
function sentView(conversation: ConversationWithState, sent: string): ConversationWithState {
  if (!conversation.is_draft && conversation.preview) return conversation;
  return {
    ...conversation,
    is_draft: false,
    draft: "",
    working: conversation.working || conversation.is_draft,
    preview: conversation.preview || sent,
  };
}

function placeholder(
  id: string,
  row: ComposerRow,
  list: ConversationWithState[],
): ConversationWithState {
  // The server reports the cwd's git state; borrow it from the latest
  // conversation in the same cwd so the row doesn't grow when that arrives.
  const sibling = row.cwd
    ? list.find((c) => c.cwd === row.cwd && !c.parent_conversation_id)
    : undefined;
  return {
    conversation_id: id,
    slug: null,
    user_initiated: true,
    created_at: row.startedAt,
    updated_at: row.startedAt,
    cwd: row.cwd,
    archived: false,
    parent_conversation_id: null,
    model: null,
    conversation_options: "{}",
    current_generation: 0,
    agent_working: row.sent !== null,
    tags: "[]",
    is_draft: row.sent === null,
    draft: "",
    queued_messages: "[]",
    turn_interrupted: false,
    external_conversation_id: null,
    external_endpoint: null,
    working: row.sent !== null,
    git_repo_root: sibling?.git_repo_root,
    git_worktree_root: sibling?.git_worktree_root,
    git_commit: sibling?.git_commit,
    git_subject: sibling?.git_subject,
    subagent_count: 0,
    running_background_jobs: 0,
    preview: row.sent ?? undefined,
    max_sequence_id: 0,
  };
}
