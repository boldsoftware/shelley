// Vue port of hooks/useDraftAutosave.ts. The original used only refs (no React
// rendering), so the logic is identical; we just replace useRef/useCallback
// with closure variables and register an onUnmounted trailing save.
import { onUnmounted } from "vue";

export interface DraftAutosaveOptions {
  baseDelayMs?: number;
  maxDelayMs?: number;
}

export interface DraftAutosaveControls {
  schedule(value: string): void;
  cancel(): void;
  flush(): void;
}

export function useDraftAutosave(
  save: (value: string) => Promise<void>,
  options: DraftAutosaveOptions = {},
): DraftAutosaveControls {
  const { baseDelayMs = 600, maxDelayMs = 10_000 } = options;
  const saveFn = save;
  // Allow the caller to swap the save fn over time without re-creating controls.
  // (Vue callers typically pass a stable closure, but keep parity with React.)

  let timer: ReturnType<typeof setTimeout> | null = null;
  let inFlight = false;
  let pendingValue: string | null = null;
  let lastSaved: string | null = null;
  let failureCount = 0;

  const computeDelay = () => {
    if (failureCount === 0) return baseDelayMs;
    return Math.min(baseDelayMs * Math.pow(2, failureCount), maxDelayMs);
  };

  const run = async () => {
    if (inFlight) return;
    const value = pendingValue;
    if (value === null) return;
    if (value === lastSaved) {
      pendingValue = null;
      return;
    }
    inFlight = true;
    try {
      await saveFn(value);
      lastSaved = value;
      failureCount = 0;
      if (pendingValue === value) pendingValue = null;
    } catch (err) {
      failureCount += 1;
      console.warn("Draft autosave failed; will retry", err);
    } finally {
      inFlight = false;
      if (pendingValue !== null) {
        if (timer) clearTimeout(timer);
        timer = setTimeout(run, computeDelay());
      }
    }
  };

  const schedule = (value: string) => {
    pendingValue = value;
    if (timer) clearTimeout(timer);
    timer = setTimeout(run, computeDelay());
  };

  const cancel = () => {
    if (timer) {
      clearTimeout(timer);
      timer = null;
    }
    pendingValue = null;
  };

  const flush = () => {
    if (timer) {
      clearTimeout(timer);
      timer = null;
    }
    void run();
  };

  onUnmounted(() => {
    if (timer) clearTimeout(timer);
    void run();
  });

  // Keep a setter parity hook (no-op for most callers).
  void saveFn;
  return { schedule, cancel, flush };
}
