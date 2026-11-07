import { ApiError } from "./api";

/** How long Shelley may be unreachable, as while it restarts, before that
 * shows as an error rather than as reconnecting. */
export const UNREACHABLE_ERROR_MS = 120_000;

/** The statuses of a server that is down or restarting behind a proxy;
 * 0 is a request that got no response at all. */
const UNREACHABLE_STATUSES = new Set([0, 502, 503, 504]);

/** Whether err is Shelley not answering: a network failure or a proxy's
 * 502, 503 or 504. */
export function isUnreachable(err: unknown): boolean {
  return err instanceof ApiError && UNREACHABLE_STATUSES.has(err.status);
}

/** What to say while Shelley has been unreachable since since: quietly
 * reconnecting (restarting, when a restart is under way) at first, an error
 * once it has been long. */
export function unreachableLine(
  since: number,
  now: number,
  restarting: boolean,
): { text: string; error: boolean } {
  const elapsed = now - since;
  if (elapsed < UNREACHABLE_ERROR_MS)
    return { text: restarting ? "Restarting…" : "Reconnecting…", error: false };
  return {
    text: `Can't reach Shelley for ${Math.floor(elapsed / 60_000)} min; retrying`,
    error: true,
  };
}
