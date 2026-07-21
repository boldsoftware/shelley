export type Politeness = "polite" | "assertive";
export type StreamStatus = "connected" | "reconnecting" | "disconnected";

export interface Announcement {
  text: string;
  politeness: Politeness;
}

export function agentAnnouncement(
  working: boolean,
  wasWorking: boolean | undefined,
  toolsCompleted = 0,
  assistantPreview = "",
): Announcement | null {
  if (working === wasWorking) return null;
  if (working) return { text: "Agent working", politeness: "polite" };
  if (wasWorking) {
    const preview = assistantPreview.trim();
    const response = preview ? ` Response: ${preview}` : "";
    if (toolsCompleted > 0) {
      const noun = toolsCompleted === 1 ? "tool" : "tools";
      return {
        text: `Agent finished. ${toolsCompleted} ${noun} completed.${response}`,
        politeness: "polite",
      };
    }
    return {
      text: preview ? `Agent finished.${response}` : "Agent finished",
      politeness: "polite",
    };
  }
  return null;
}

export function streamAnnouncement(
  status: StreamStatus,
  previous: StreamStatus | undefined,
): Announcement | null {
  if (status === previous) return null;
  if (status === "reconnecting") return { text: "Reconnecting", politeness: "polite" };
  if (status === "disconnected") return { text: "Disconnected", politeness: "assertive" };
  if (previous === "disconnected" || previous === "reconnecting") {
    return { text: "Connected", politeness: "polite" };
  }
  return null;
}

export function errorAnnouncement(
  error: string | null,
  previous: string | null | undefined,
): Announcement | null {
  if (error === previous) return null;
  if (error) return { text: error, politeness: "assertive" };
  if (previous) return { text: "", politeness: "polite" };
  return null;
}
