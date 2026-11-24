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
  // Whether a value waits to be saved, or is being saved.
  pending(): boolean;
  // The server holds value (another client saved it, or a send cleared it),
  // so saving it again is a no-op. null: what it holds is unknown, so the
  // next value saves even if this composer saved it last.
  observe(value: string | null): void;
  // Starts over for another composer: drops the debounce and what was saved,
  // and returns the value still waiting to be saved (say, behind a save in
  // flight), if any, for the caller to save where it belongs. The save in
  // flight finishes without touching the new state.
  reset(): string | null;
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
  let inFlightValue: string | null = null;
  let generation = 0;
  let inFlightGeneration = 0;
  let observed = 0;
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
    inFlightValue = value;
    inFlightGeneration = generation;
    const started = generation;
    const observedBefore = observed;
    try {
      await saveFn(value);
      if (started === generation) {
        // Unless the server since had other text (observe).
        if (observed === observedBefore) lastSaved = value;
        failureCount = 0;
        if (pendingValue === value) pendingValue = null;
      }
    } catch (err) {
      if (started === generation) failureCount += 1;
      console.warn("Draft autosave failed; will retry", err);
    } finally {
      inFlight = false;
      inFlightValue = null;
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
  return {
    schedule,
    cancel,
    flush,
    pending: () => pendingValue !== null,
    observe: (value) => {
      lastSaved = value;
      observed++;
    },
    reset: () => {
      // What the server ends up with from this composer: the save in
      // flight, else the last one.
      const saving = inFlight && inFlightGeneration === generation;
      const server = saving ? inFlightValue : lastSaved;
      const waiting = pendingValue === server ? null : pendingValue;
      cancel();
      lastSaved = null;
      failureCount = 0;
      generation++;
      return waiting;
    },
  };
}
