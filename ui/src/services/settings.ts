import { workspaceMode } from "../utils/workspaceMode";

const MARKDOWN_KEY = "shelley-markdown-rendering";
const CONVERSATION_VIEW_KEY = "shelley-conversation-view";
const MESSAGE_USER_VIEW_KEY = "shelley-message-user-view";

export type MarkdownMode = "off" | "agent" | "all";
export type ConversationViewMode = "all" | "end-of-turn" | "brief";
// Conversations with the message_user tool have their own view setting, which
// defaults to showing only the chat.
export type MessageUserViewMode = "all" | "brief";

export function getMarkdownMode(): MarkdownMode {
  const val = localStorage.getItem(MARKDOWN_KEY);
  // Migrate old boolean values
  if (val === "true") return "agent";
  if (val === "false") return "off";
  if (val === "agent" || val === "all" || val === "off") return val;
  return "agent"; // default
}

export function setMarkdownMode(mode: MarkdownMode): void {
  localStorage.setItem(MARKDOWN_KEY, mode);
}

// Until the user picks a view, workspace mode starts conversations in the
// brief one (end of turn); stock Shelley shows everything.
export function getConversationViewMode(): "all" | "end-of-turn" {
  const val = localStorage.getItem(CONVERSATION_VIEW_KEY);
  if (val === "all" || val === "end-of-turn") return val;
  return workspaceMode ? "end-of-turn" : "all";
}

export function setConversationViewMode(mode: "all" | "end-of-turn"): void {
  localStorage.setItem(CONVERSATION_VIEW_KEY, mode);
}

export function getMessageUserViewMode(): MessageUserViewMode {
  return localStorage.getItem(MESSAGE_USER_VIEW_KEY) === "all" ? "all" : "brief";
}

export function setMessageUserViewMode(mode: MessageUserViewMode): void {
  localStorage.setItem(MESSAGE_USER_VIEW_KEY, mode);
}
