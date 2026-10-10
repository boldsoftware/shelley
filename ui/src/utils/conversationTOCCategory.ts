import { isDistillStatusMessage, type LLMContent, type Message } from "../types";
import { isTypedUserMessage, MESSAGE_USER_TOOL, messageUserInput } from "./conversationView";
import { messageSource } from "./messageSource";

export const TOC_CATEGORIES = {
  user: { label: "User prompt", icon: "•" },
  reply: { label: "Model reply", icon: "✓" },
  progress: { label: "Model update", icon: "·" },
  subagent: { label: "Agent report", icon: "↳" },
  "tool-result": { label: "Tool result", icon: "↩" },
  background: { label: "Background-job update", icon: "↻" },
  notice: { label: "System notice", icon: "–" },
} as const;

export type TOCCategory = keyof typeof TOC_CATEGORIES;

export function messageTOCCategory(message: Message, content: readonly LLMContent[]): TOCCategory {
  if (isDistillStatusMessage(message)) return "notice";

  if (message.type === "user") {
    const source = messageSource(message.user_data);
    if (source) return "backgroundJobId" in source ? "background" : "subagent";
    if (content.some((item) => item.Type === 6 || item.Type === 8)) return "tool-result";
    return isTypedUserMessage(message) ? "user" : "notice";
  }

  if (message.type === "agent") {
    return message.end_of_turn ? "reply" : "progress";
  }
  return "notice";
}

export function toolTOCCategory(toolUse: LLMContent): TOCCategory {
  if (toolUse.ToolName === MESSAGE_USER_TOOL) {
    return messageUserInput(toolUse.ToolInput).end_turn ? "reply" : "progress";
  }
  // Tool thumbnails come from completed results, not pending calls.
  return "tool-result";
}
