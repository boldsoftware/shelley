import type { Message } from "../types";
import { isTypedUserMessage } from "./conversationView";
import { messageSource, waitNudgeSource } from "./messageSource";

export type TOCCategory = "user" | "subagent" | "background" | "notice";

// Who a user-role message came from: typed by the user, or delivered as one.
export function messageTOCCategory(message: Message): TOCCategory {
  const source = messageSource(message.user_data);
  if (source) return "backgroundJobId" in source ? "background" : "subagent";
  const nudge = waitNudgeSource(message.user_data);
  if (nudge) return nudge.kind === "background_job" ? "background" : "subagent";
  return isTypedUserMessage(message) ? "user" : "notice";
}
