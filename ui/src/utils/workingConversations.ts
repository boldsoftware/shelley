import type { ConversationWithState } from "../types";

// Display activity includes delegated work; foreground busy controls do not.
export function workingConversationIds(conversations: ConversationWithState[]): Set<string> {
  const parents = new Map(conversations.map((c) => [c.conversation_id, c.parent_conversation_id]));
  const working = new Set<string>();
  for (const conversation of conversations) {
    if (!conversation.working && !(conversation.running_background_jobs > 0)) continue;
    let id: string | null | undefined = conversation.conversation_id;
    while (id && !working.has(id)) {
      working.add(id);
      id = parents.get(id);
    }
  }
  return working;
}
