import { initializeFavicon } from "../favicon";
import { registerHandler } from "./handlers";
import { faviconNotificationHandler } from "./handlers/favicon";
import { browserNotificationHandler } from "./handlers/browser";
import { setChannelEnabled } from "./preferences";
import { pushApi } from "../api";

export { handleNotificationEvent } from "./handlers";
export { isChannelEnabled, setChannelEnabled } from "./preferences";

export function initializeNotifications(): void {
  initializeFavicon();
  registerHandler("favicon", faviconNotificationHandler);
  registerHandler("browser", browserNotificationHandler);
  registerServiceWorker();
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

// --- Service Worker & Web Push ---

export type WebPushState = "unsupported" | "subscribed" | "unsubscribed" | "denied";

export async function getWebPushState(): Promise<WebPushState> {
  if (!("serviceWorker" in navigator) || !("PushManager" in window)) return "unsupported";
  if (Notification.permission === "denied") return "denied";
  try {
    const reg = await navigator.serviceWorker.ready;
    const sub = await reg.pushManager.getSubscription();
    return sub ? "subscribed" : "unsubscribed";
  } catch {
    return "unsubscribed";
  }
}

export async function subscribeToWebPush(): Promise<boolean> {
  if (!("serviceWorker" in navigator) || !("PushManager" in window)) return false;

  const permission = await Notification.requestPermission();
  if (permission !== "granted") return false;

  try {
    const vapidPublicKey = await pushApi.getVapidPublicKey();
    const reg = await navigator.serviceWorker.ready;
    const sub = await reg.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: urlBase64ToUint8Array(vapidPublicKey),
    });
    await pushApi.subscribe(sub.toJSON() as PushSubscriptionJSON);
    return true;
  } catch (err) {
    console.error("Web push subscription failed:", err);
    return false;
  }
}

export async function unsubscribeFromWebPush(): Promise<void> {
  if (!("serviceWorker" in navigator)) return;
  try {
    const reg = await navigator.serviceWorker.ready;
    const sub = await reg.pushManager.getSubscription();
    if (sub) {
      await pushApi.unsubscribe(sub.endpoint);
      await sub.unsubscribe();
    }
  } catch (err) {
    console.error("Web push unsubscribe failed:", err);
  }
}

function registerServiceWorker(): void {
  if (!("serviceWorker" in navigator)) return;
  navigator.serviceWorker.register("/sw.js", { scope: "/" }).catch((err) => {
    console.warn("Service worker registration failed:", err);
  });
}

function urlBase64ToUint8Array(base64String: string): Uint8Array {
  const padding = "=".repeat((4 - (base64String.length % 4)) % 4);
  const base64 = (base64String + padding).replace(/-/g, "+").replace(/_/g, "/");
  const rawData = atob(base64);
  return Uint8Array.from([...rawData].map((c) => c.charCodeAt(0)));
}
