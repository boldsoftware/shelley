import type { LLMContent, Message } from "../types";
import { isDistillStatusMessage } from "../types";
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

  let human = true;
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

export function isVisibleConversationMessage(
  message: Message,
  mode: ConversationViewMode,
): boolean {
  if (mode === "all") return true;
  if (isHumanUserMessage(message)) return true;
  if (isDistillStatusMessage(message)) return true;
  if (message.type === "agent") return !!message.end_of_turn;
  return ["error", "warning", "gitinfo", "modelchange"].includes(message.type);
}
