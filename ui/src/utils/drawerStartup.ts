import type { Conversation } from "../types";

type DrawerConversation = Pick<Conversation, "parent_conversation_id">;

export function shouldStartDrawerCollapsed(conversations: DrawerConversation[]): boolean {
  let topLevelCount = 0;
  for (const conversation of conversations) {
    if (conversation.parent_conversation_id) continue;
    topLevelCount += 1;
    if (topLevelCount > 1) return false;
  }
  return true;
}
