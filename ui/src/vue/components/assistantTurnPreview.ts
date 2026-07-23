/** Pure helper: plain-text preview of the assistant response for the current turn only. */

export type PreviewMessage = {
  type: string;
  message_id: string;
  llm_data?: unknown;
};

export type ContentPart = {
  Type?: number;
  Text?: string;
};

const TEXT_TYPE = 2;

function agentText(message: PreviewMessage): string {
  try {
    const raw = message.llm_data;
    const llm =
      typeof raw === "string" ? (JSON.parse(raw) as { Content?: ContentPart[] }) : (raw as { Content?: ContentPart[] });
    return (llm?.Content || [])
      .filter((content) => content.Type === TEXT_TYPE && content.Text)
      .map((content) => content.Text!.trim())
      .filter(Boolean)
      .join("\n\n");
  } catch {
    return "";
  }
}

/**
 * Returns a short plain preview of the assistant's text in the *current* turn
 * (messages after the latest user message). Never falls back to an earlier
 * turn — that caused Safari VO to announce a previous response on finish.
 */
export function currentTurnAssistantPreview(
  messages: PreviewMessage[],
  strip: (messageId: string, text: string) => string,
  maxLen = 180,
): string {
  let lastUser = -1;
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i].type === "user") {
      lastUser = i;
      break;
    }
  }

  for (let i = messages.length - 1; i > lastUser; i--) {
    const message = messages[i];
    if (message.type !== "agent") continue;
    const text = agentText(message);
    if (!text) continue;
    const plainText = strip(message.message_id, text);
    if (!plainText) continue;
    return plainText.length > maxLen ? `${plainText.slice(0, maxLen - 3).trimEnd()}…` : plainText;
  }
  return "";
}
