export type A11yPoliteness = "polite" | "assertive";

export const A11Y_ANNOUNCE_EVENT = "shelley:a11y-announce";

export interface A11yAnnouncementDetail {
  text: string;
  politeness: A11yPoliteness;
}

/** Sends one message through the app-level StatusAnnouncer live region. */
export function announceA11y(text: string, politeness: A11yPoliteness = "polite") {
  if (!text || typeof window === "undefined") return;
  window.dispatchEvent(
    new CustomEvent<A11yAnnouncementDetail>(A11Y_ANNOUNCE_EVENT, {
      detail: { text, politeness },
    }),
  );
}
