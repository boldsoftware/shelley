/** Interaction events that show the user is here. */
export const ACTIVITY_EVENTS = [
  "pointerdown",
  "pointermove",
  "keydown",
  "wheel",
  "scroll",
  "touchstart",
  "focus",
] as const;
export const ACTIVITY_THROTTLE_MS = 5000;

function sendActivity() {
  fetch("/api/activity", { method: "POST", headers: { "X-Shelley-Request": "1" } }).catch(
    (err: unknown) => console.warn("Activity heartbeat failed", err),
  );
}

/**
 * Tells the server, at most every ACTIVITY_THROTTLE_MS, that the user is
 * interacting, so Shelley never auto-restarts mid-read. Returns a stop function.
 */
export function startActivityHeartbeat(target: EventTarget = window, send = sendActivity) {
  let last = -Infinity;
  const beat = () => {
    const now = Date.now();
    if (now - last < ACTIVITY_THROTTLE_MS) return;
    last = now;
    send();
  };
  const options = { capture: true, passive: true };
  for (const event of ACTIVITY_EVENTS) target.addEventListener(event, beat, options);
  return () => {
    for (const event of ACTIVITY_EVENTS) target.removeEventListener(event, beat, options);
  };
}
