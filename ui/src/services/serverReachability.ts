import { readonly, ref } from "vue";
import { isUnreachable } from "./unreachable";

const since = ref<number | null>(null);
/** When requests to Shelley began failing as unreachable, or null while it
 * answers. The page shows this one state; the requests that saw it don't. */
export const unreachableSince = readonly(since);

export function serverUnreachable(at = Date.now()) {
  since.value ??= at;
}

export function serverReachable() {
  since.value = null;
}

/** For a background request's failure: true, after noting it, when err
 * means Shelley is unreachable, which unreachableSince shows instead. */
export function absorbUnreachable(err: unknown): boolean {
  if (!isUnreachable(err)) return false;
  console.warn("Shelley is unreachable:", err);
  serverUnreachable();
  return true;
}
