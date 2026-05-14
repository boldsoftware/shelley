// Restore keyboard focus to the chat message input after a modal/overlay closes.
//
// Only refocuses when focus has fallen back to <body> (i.e. the closing
// element was the focus holder). That way we don't steal focus from a
// follow-up modal that the closing element launched, or from a deliberate
// user click somewhere else.
//
// On mobile we skip refocus to avoid popping up the soft keyboard.
export function focusMessageInputIfUnfocused(): void {
  const isMobile = typeof window !== "undefined" && "ontouchstart" in window;
  if (isMobile) return;
  // Defer so it runs after React commits and any modal-being-opened has
  // claimed focus first.
  setTimeout(() => {
    if (typeof document === "undefined") return;
    if (document.activeElement && document.activeElement !== document.body) return;
    const input = document.querySelector<HTMLTextAreaElement>('[data-testid="message-input"]');
    input?.focus();
  }, 0);
}
