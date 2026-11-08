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

// A folded project marks the work its hidden rows would show; unfolded, the
// rows show it themselves.
export function foldedProjectWorking(
  folded: boolean,
  rows: { conversation_id: string }[],
  working: Set<string>,
): boolean {
  return folded && rows.some((row) => working.has(row.conversation_id));
}

// Workspace mode's favicon dot: any conversation working as the nav shows it
// (the Assistant and subagents included), or any project action running.
export function anyTaskRunning(
  conversations: ConversationWithState[],
  runs: { status: string }[],
): boolean {
  return (
    conversations.some((c) => c.working || c.running_background_jobs > 0) ||
    runs.some((run) => run.status === "running")
  );
}
