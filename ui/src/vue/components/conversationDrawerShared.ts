// Shared types/helpers/injection key for ConversationDrawer.vue and its row
// child ConversationDrawerRow.vue. Mirrors the snippet/tags helpers from the
// React components/ConversationDrawer.tsx.
import type { ComputedRef, InjectionKey, Ref } from "vue";
import type { Conversation, ConversationWithState } from "../../types";
import type { TranslationKeys } from "../../i18n/types";

export type GroupBy = "none" | "cwd" | "git_repo";

// Parses the JSON-encoded tags field on a Conversation. Tolerates the empty
// string and malformed JSON (treated as no tags).
export function parseTags(conversation: Conversation): string[] {
  if (!conversation.tags) return [];
  try {
    const parsed = JSON.parse(conversation.tags);
    return Array.isArray(parsed) ? parsed.filter((t) => typeof t === "string") : [];
  } catch {
    return [];
  }
}

// SNIPPET_MARK_START / END match db.SnippetMarkStart / SnippetMarkEnd on the
// server. The server wraps every matched FTS term in these sentinel bytes.
export const SNIPPET_MARK_START = "\x02";
export const SNIPPET_MARK_END = "\x03";

export function stripSnippetMarks(snippet: string): string {
  return snippet.split(SNIPPET_MARK_START).join("").split(SNIPPET_MARK_END).join("");
}

export interface SnippetSegment {
  text: string;
  mark: boolean;
}

// Split a snippet into plain/marked runs so the row can render <mark> spans
// without v-html. Mirrors renderSnippet() in the React component.
export function renderSnippetSegments(snippet: string): SnippetSegment[] {
  const out: SnippetSegment[] = [];
  let i = 0;
  while (i < snippet.length) {
    const start = snippet.indexOf(SNIPPET_MARK_START, i);
    if (start === -1) {
      out.push({ text: snippet.slice(i), mark: false });
      break;
    }
    if (start > i) out.push({ text: snippet.slice(i, start), mark: false });
    const end = snippet.indexOf(SNIPPET_MARK_END, start + 1);
    if (end === -1) {
      out.push({ text: snippet.slice(start + 1), mark: false });
      break;
    }
    out.push({ text: snippet.slice(start + 1, end), mark: true });
    i = end + 1;
  }
  return out;
}

// The context shared from ConversationDrawer.vue down to its row children.
export interface DrawerCtx {
  t: (key: keyof TranslationKeys) => string;
  currentConversationId: ComputedRef<string | null>;
  subagentsByParent: ComputedRef<Record<string, ConversationWithState[]>>;
  expandedSubagents: Ref<Set<string>>;
  seenIds: Ref<Set<string> | null>;
  copiedConvId: Ref<string | null>;
  pendingDeleteId: Ref<string | null>;
  pendingDeleteRef: Ref<HTMLElement | null>;
  editingId: Ref<string | null>;
  editingSlug: Ref<string>;
  renameInputRef: Ref<HTMLInputElement | null>;
  tagEditorId: Ref<string | null>;
  tagInput: Ref<string>;
  tagEditorRef: Ref<HTMLElement | null>;
  tagInputRef: Ref<HTMLInputElement | null>;
  draftLabels: ComputedRef<Record<string, string>>;
  groupBy: Ref<GroupBy>;
  formatDate: (timestamp: string) => string;
  formatCwdForDisplay: (p: string | null | undefined) => string | null;
  handleModifiedClick: (e: MouseEvent, conversation: Conversation) => boolean;
  handleAuxClick: (e: MouseEvent, conversation: Conversation) => void;
  selectConversation: (c: Conversation) => void;
  toggleSubagents: (e: MouseEvent, conversationId: string) => void;
  handleStartRename: (e: MouseEvent, conversation: Conversation) => void;
  handleRename: (conversationId: string) => void;
  handleRenameKeyDown: (e: KeyboardEvent, conversationId: string) => void;
  handleOpenTagEditor: (e: MouseEvent, conversationId: string) => void;
  handleAddTag: (conversation: Conversation) => void;
  handleRemoveTag: (conversation: Conversation, tag: string) => void;
  handleArchive: (e: MouseEvent, conversationId: string) => void;
  handleUnarchive: (e: MouseEvent, conversationId: string) => void;
  handleCopyGitHash: (e: MouseEvent, hash: string, convId: string) => void;
  handleDeleteClick: (e: MouseEvent, conversationId: string) => void;
  handleConfirmDelete: (e: MouseEvent, conversationId: string) => void;
  handleCancelDelete: (e: MouseEvent) => void;
}

export const DrawerCtxKey: InjectionKey<DrawerCtx> = Symbol("shelley-conversation-drawer");
