// Tiny client-side cache for feature-flag values.
//
// Most call sites just want "is this flag on?" — they don't care about the
// description or override metadata that powers FeatureFlagsModal. This module
// owns one fetch of `/feature-flags`, caches the boolean value of each flag,
// and exposes a hook that components can use synchronously after the initial
// load. The FeatureFlagsModal calls `refresh()` after mutating overrides so
// changes propagate immediately without a page reload.

import { useEffect, useState } from "react";
import { featureFlagsApi, type FeatureFlag } from "./api";

type Listener = (values: Record<string, unknown>) => void;

let values: Record<string, unknown> = {};
let loaded = false;
let inflight: Promise<void> | null = null;
const listeners = new Set<Listener>();

function notify() {
  for (const l of listeners) l(values);
}

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
      values = buildValues(flags);
      loaded = true;
      notify();
    } catch (e) {
      // Leave `loaded` false so callers fall back to defaults. We deliberately
      // swallow the error: a flag fetch failure should never break the UI.
      console.warn("featureFlagsStore: load failed", e);
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
// `page.addInitScript` is private to that page. Manual users can flip it
// too via the devtools console (`localStorage['ff:tool-pills']='true'`).
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

/** Subscribe to a single boolean flag. Returns `fallback` until the initial
 *  fetch completes or if the flag is missing / non-boolean. A localStorage
 *  value at `ff:<name>` ("true"/"false") takes precedence over the server. */
export function useFeatureFlag(name: string, fallback = false): boolean {
  const [, force] = useState(0);
  useEffect(() => {
    const l: Listener = () => force((n) => n + 1);
    listeners.add(l);
    if (!loaded && !inflight) void load();
    return () => {
      listeners.delete(l);
    };
  }, []);
  const override = localOverride(name);
  if (override !== undefined) return override;
  if (!loaded) return fallback;
  const v = values[name];
  return typeof v === "boolean" ? v : fallback;
}
