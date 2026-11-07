import { apiJSON } from "./api";

/** Why a page can't be framed here: it is Shelley itself, its
 *  Content-Security-Policy or X-Frame-Options forbids it, or it is behind
 *  the exe.dev login on another site. */
export type FrameBlocked = "shelley" | "csp" | "x-frame-options" | "login";

/** Whether the browser can frame a page on this page's origin. */
export interface PageFrame {
  /** Why it can't, or "": then it opens in a new browser tab. */
  blocked: FrameBlocked | "";
  /** Behind the exe.dev login, whose cookie the frame gets once the user has
   *  logged in to the page in a tab of its own. */
  login?: boolean;
}

const probes = new Map<string, Promise<PageFrame>>();

function fetchPageFrame(url: string, failure: string): Promise<PageFrame> {
  return apiJSON(`/api/page-frame?url=${encodeURIComponent(url)}`, failure);
}

/** Asks the server once per page load, or again with refresh; failure
 *  prefixes its error. Shelley's own origin is never framed: the frame
 *  would share its storage. */
export function pageFrame(url: string, failure: string, refresh = false): Promise<PageFrame> {
  if (new URL(url).origin === window.location.origin)
    return Promise.resolve({ blocked: "shelley" });
  let probe = probes.get(url);
  if (!probe || refresh) {
    probe = fetchPageFrame(url, failure);
    probes.set(url, probe);
    probe.catch(() => probes.delete(url));
  }
  return probe;
}
