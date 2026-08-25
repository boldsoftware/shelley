const MARKDOWN_KEY = "shelley-markdown-rendering";
const CONVERSATION_VIEW_KEY = "shelley-conversation-view";

export type MarkdownMode = "off" | "agent" | "all";
export type ConversationViewMode = "all" | "end-of-turn";

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

export function getConversationViewMode(): ConversationViewMode {
  return localStorage.getItem(CONVERSATION_VIEW_KEY) === "end-of-turn" ? "end-of-turn" : "all";
}

export function setConversationViewMode(mode: ConversationViewMode): void {
  localStorage.setItem(CONVERSATION_VIEW_KEY, mode);
}
