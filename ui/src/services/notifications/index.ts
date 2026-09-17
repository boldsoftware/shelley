import { initializeFavicon } from "../favicon";
import { registerHandler } from "./handlers";
import { browserNotificationHandler } from "./handlers/browser";
import { setChannelEnabled } from "./preferences";

export { handleNotificationEvent } from "./handlers";
export { isChannelEnabled, setChannelEnabled } from "./preferences";

export function initializeNotifications(): void {
  initializeFavicon();
  registerHandler("browser", browserNotificationHandler);
}

export type BrowserNotificationState = "unsupported" | "granted" | "denied" | "default";

export function getBrowserNotificationState(): BrowserNotificationState {
  if (typeof Notification === "undefined") return "unsupported";
  return Notification.permission;
}

export async function requestBrowserNotificationPermission(): Promise<boolean> {
  if (typeof Notification === "undefined") return false;

  const result = await Notification.requestPermission();
  if (result === "granted") {
    setChannelEnabled("browser", true);
    return true;
  }
  return false;
}
