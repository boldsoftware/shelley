import { onUnmounted, ref, shallowRef } from "vue";
import { customizationsApi, type Customization } from "../../services/customizations";
import { errorMessage } from "../../utils/errorMessage";
import { workspaceMode } from "../../utils/workspaceMode";

/** A single nav poll also detects external checkout changes. A restart
 * reloads the page through the UI build check once the new server answers. */
export function createCustomizationsState(isVisible: () => boolean = () => true) {
  const entries = shallowRef<Customization[]>([]);
  const error = ref("");
  const requestError = ref("");
  const pendingConversation = ref("");
  let timer: ReturnType<typeof setTimeout> | null = null;
  let pending: Promise<void> | null = null;
  let stopped = false;
  let revision = 0;

  function pause() {
    if (timer !== null) clearTimeout(timer);
    timer = null;
  }

  function schedule() {
    pause();
    if (!stopped && isVisible())
      timer = setTimeout(
        () => void refresh(),
        entries.value.some(
          (entry) =>
            entry.status === "verifying" ||
            entry.status === "waiting" ||
            entry.auto_restart === "pending",
        )
          ? 1500
          : 5000,
      );
  }

  function refresh(): Promise<void> {
    if (pending) return pending;
    const requestedRevision = revision;
    pending = customizationsApi
      .list()
      .then((next) => {
        if (stopped || requestedRevision !== revision) return;
        entries.value = next;
        error.value = "";
      })
      .catch((err: unknown) => {
        if (stopped) return;
        error.value = errorMessage(err);
      })
      .finally(() => {
        pending = null;
        schedule();
      });
    return pending;
  }

  async function mutate(
    state: Customization,
    operation: "verify" | "restart" | "cancelAutoRestart",
  ) {
    if (pendingConversation.value) return;
    pendingConversation.value = state.conversation_id;
    revision++;
    requestError.value = "";
    try {
      const next = await customizationsApi[operation](state);
      if (!stopped)
        entries.value = [
          ...entries.value.filter((entry) => entry.conversation_id !== next.conversation_id),
          next,
        ];
    } catch (err) {
      requestError.value = errorMessage(err);
    } finally {
      revision++;
      pendingConversation.value = "";
      // A disconnected POST is ambiguous. Reconcile persistent server state,
      // never retry an install request automatically.
      await pending;
      await refresh();
    }
  }

  function dispose() {
    stopped = true;
    pause();
  }

  return { entries, error, requestError, pendingConversation, refresh, mutate, pause, dispose };
}

export function useCustomizations() {
  const state = createCustomizationsState(() => !document.hidden);
  // Only workspace mode has the Shelley project that customizes Shelley.
  if (!workspaceMode) return state;
  const refresh = () => {
    if (!document.hidden) void state.refresh();
  };
  const visibility = () => {
    if (document.hidden) state.pause();
    else refresh();
  };
  document.addEventListener("visibilitychange", visibility);
  window.addEventListener("focus", refresh);
  refresh();
  onUnmounted(() => {
    state.dispose();
    document.removeEventListener("visibilitychange", visibility);
    window.removeEventListener("focus", refresh);
  });
  return state;
}
