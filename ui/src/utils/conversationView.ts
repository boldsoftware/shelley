import type { LLMContent, Message } from "../types";
import { isDistillStatusMessage } from "../types";
import { conversationMessageSource } from "./messageSource";
import type { ConversationViewMode } from "../services/settings";

const LLM_TYPE_TOOL_RESULT = 6;
const humanUserCache = new Map<string, boolean>();

export function clearConversationViewCache(): void {
  humanUserCache.clear();
}

export function isHumanUserMessage(message: Message): boolean {
  if (message.type !== "user") return false;
  const cached = humanUserCache.get(message.message_id);
  if (cached !== undefined) return cached;

  let human = !conversationMessageSource(message.user_data);
  if (message.user_data) {
    try {
      const userData =
        typeof message.user_data === "string" ? JSON.parse(message.user_data) : message.user_data;
      if (userData?.distilled === "true") human = false;
    } catch {
      // Malformed metadata should not hide a message.
    }
  }
  if (human && message.llm_data) {
    try {
      const llm =
        typeof message.llm_data === "string" ? JSON.parse(message.llm_data) : message.llm_data;
      const content: LLMContent[] = llm?.Content || [];
      human = !content.some((item) => item.Type === LLM_TYPE_TOOL_RESULT);
    } catch {
      // Malformed user content is still safer to show than to hide.
    }
  }
  humanUserCache.set(message.message_id, human);
  return human;
}

// The user_data a compaction stamps on the copies it carries forward.
const CARRIED_KEYS = new Set(["compaction_carried", "carried_from_sequence_id"]);

// A message the user typed, as opposed to a user-role message from a subagent,
// a background job, or another machine source (which carry user_data). Copies
// carried forward by a compaction still count; they render behind a band.
export function isTypedUserMessage(message: Message): boolean {
  if (!isHumanUserMessage(message)) return false;
  if (!message.user_data) return true;
  try {
    const userData =
      typeof message.user_data === "string" ? JSON.parse(message.user_data) : message.user_data;
    return !userData || Object.keys(userData).every((key) => CARRIED_KEYS.has(key));
  } catch {
    // As in isHumanUserMessage: malformed metadata should not hide a message.
    return true;
  }
}

export function isVisibleConversationMessage(
  message: Message,
  mode: ConversationViewMode,
): boolean {
  if (mode === "all") return true;
  if (mode === "brief") {
    if (isTypedUserMessage(message)) return true;
    return ["error", "warning", "modelchange"].includes(message.type);
  }
  if (isHumanUserMessage(message)) return true;
  if (isDistillStatusMessage(message)) return true;
  if (message.type === "agent") return !!message.end_of_turn;
  return ["error", "warning", "gitinfo", "modelchange", "inplacecompaction"].includes(message.type);
}

export const MESSAGE_USER_TOOL = "message_user";

/** Input of a message_user call, as the model sent it. */
export interface MessageUserInput {
  text?: string;
  reply_to?: number;
  reaction?: string;
  attachments?: string[];
  end_turn?: boolean;
}

/** Display of a message_user call (claudetool.MessageUserDisplay). */
export interface MessageUserDisplay {
  target_message_id?: string;
  target_sequence_id?: number;
  target_excerpt?: string;
  attachments?: { path: string; name: string; size: number }[];
  /** Set on a call that failed in the user's chat (e.g. their phone). */
  chat_failed?: boolean;
}

export function messageUserInput(toolInput: unknown): MessageUserInput {
  return typeof toolInput === "object" && toolInput !== null ? (toolInput as MessageUserInput) : {};
}

export function messageUserDisplay(display: unknown): MessageUserDisplay {
  return typeof display === "object" && display !== null ? (display as MessageUserDisplay) : {};
}

/** Whether a message_user call delivered a message (not just a reaction),
 * which brief view shows. Reactions show on the message they react to. */
export function isDeliveredUserMessage(item: {
  toolName?: string;
  toolInput?: unknown;
  hasResult?: boolean;
  toolError?: boolean;
  display?: unknown;
}): boolean {
  if (item.toolName !== MESSAGE_USER_TOOL || !item.hasResult || item.toolError) return false;
  const input = messageUserInput(item.toolInput);
  return !!input.text?.trim() || !!messageUserDisplay(item.display).attachments?.length;
}

/** Whether a message_user call failed in the user's chat, such as a phone
 * the messages service refused to send to. Brief view shows it: unlike a
 * mistake in the call, the agent cannot fix it, so it may show nothing else. */
export function isFailedChatDelivery(item: {
  toolName?: string;
  hasResult?: boolean;
  toolError?: boolean;
  display?: unknown;
}): boolean {
  return (
    item.toolName === MESSAGE_USER_TOOL &&
    !!item.hasResult &&
    !!item.toolError &&
    !!messageUserDisplay(item.display).chat_failed
  );
}
