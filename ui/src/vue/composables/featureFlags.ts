// Vue port of services/featureFlagsStore.ts. A module-level reactive cache of
// feature-flag values backed by one fetch of `/feature-flags`. Mirrors the
// React store's behavior exactly: a `ff:<name>` localStorage value
// ("true"/"false") takes precedence over the server, callers fall back to a
// default until the initial load completes, and FeatureFlagsModal calls
// `refreshFeatureFlags()` after mutating overrides so changes propagate.
import { ref, computed, type Ref } from "vue";
import { featureFlagsApi, type FeatureFlag } from "../../services/api";

const values = ref<Record<string, unknown>>({});
const loaded = ref(false);
let inflight: Promise<void> | null = null;

function buildValues(flags: FeatureFlag[]): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const f of flags) {
    out[f.name] = f.override !== undefined ? f.override : f.default;
  }
  return out;
}

async function load(): Promise<void> {
  if (inflight) return inflight;
  inflight = (async () => {
    try {
      const flags = await featureFlagsApi.list();
      values.value = buildValues(flags);
      loaded.value = true;
    } catch (e) {
      // Leave `loaded` false so callers fall back to defaults. We deliberately
      // swallow the error: a flag fetch failure should never break the UI.
      console.warn("featureFlags: load failed", e);
    } finally {
      inflight = null;
    }
  })();
  return inflight;
}

/** Force a re-fetch and notify all listeners. Used by FeatureFlagsModal
 *  after the user toggles an override. */
export async function refreshFeatureFlags(): Promise<void> {
  inflight = null;
  await load();
}

// Per-page localStorage override, scoped to a single browser tab. This is
// here mainly for E2E tests: writing the global DB-backed override races
// across parallel Playwright workers, but a localStorage value set via
// `page.addInitScript` is private to that page.
function localOverride(name: string): boolean | undefined {
  if (typeof window === "undefined") return undefined;
  let raw: string | null = null;
  try {
    raw = window.localStorage.getItem(`ff:${name}`);
  } catch {
    return undefined;
  }
  if (raw === "true") return true;
  if (raw === "false") return false;
  return undefined;
}

/** Subscribe to a single boolean flag. Returns a Ref that yields `fallback`
 *  until the initial fetch completes or if the flag is missing / non-boolean.
 *  A localStorage value at `ff:<name>` ("true"/"false") takes precedence. */
export function useFeatureFlag(name: string, fallback = false): Ref<boolean> {
  if (!loaded.value && !inflight) void load();
  return computed(() => {
    const override = localOverride(name);
    if (override !== undefined) return override;
    if (!loaded.value) return fallback;
    const v = values.value[name];
    return typeof v === "boolean" ? v : fallback;
  });
}
