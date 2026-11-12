// Temporary conversations stay out of the Shelley archive store (server/archiver.go).
// The choice is made in the composer before the first message, and only
// while this Shelley pushes to a store (its "archiver" flag is a URL).

/** Whether the archiver flag's value turns pushing on. */
export function archiverOn(value: unknown): boolean {
  return typeof value === "string" && value !== "";
}

/** Whether a conversation's options (the JSON string the API sends) mark it
 *  temporary. */
export function isTemporaryConversation(
  conv: { conversation_options?: string } | null | undefined,
): boolean {
  if (!conv?.conversation_options) return false;
  try {
    return JSON.parse(conv.conversation_options)?.temporary === true;
  } catch {
    return false;
  }
}

/** What the composer's Temporary toggle applies to: the new conversation,
 *  or the next message (the Assistant's; see server/temporary.go). */
export type TemporaryScope = "conversation" | "message";

/** The composer toggle's tooltip, saying what happens to what it sends. */
export function temporaryTooltip(
  temporary: boolean,
  scope: TemporaryScope = "conversation",
): string {
  if (scope === "message") {
    return temporary
      ? "Temporary: this message, the replies to it and any conversations it starts stay out of the archive. Turns off once sent."
      : "This message and the replies to it will be archived, where your team can read them. Turn on Temporary to keep this one out.";
  }
  return temporary
    ? "Temporary: this conversation stays on this machine and isn't archived."
    : "This conversation will be archived to the Shelley archive store, where anyone with access to the store can read it. Turn on Temporary to keep it out.";
}
