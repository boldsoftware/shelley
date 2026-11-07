import { computed, ref, shallowRef } from "vue";
import { customizationsApi, type Customization } from "../../services/customizations";
import { unreachableSince } from "../../services/serverReachability";
import { errorMessage } from "../../utils/errorMessage";

/** Where a build's buttons were clicked: its failure shows there. */
export type CustomizationsPlace = "drawer" | "header";

/** Stamps when each automatic restart is due, by this browser's clock. */
function received(state: Customization): Customization {
  return state.auto_restart_in_ms === undefined
    ? state
    : { ...state, auto_restart_at: Date.now() + state.auto_restart_in_ms };
}

/**
 * The page's build states. The event stream sends them on connect and on
 * every change (applyCustomizations); nothing polls.
 */
export function createCustomizationsState() {
  const entries = shallowRef<Customization[]>([]);
  const requestError = ref<{
    message: string;
    conversationId: string;
    place: CustomizationsPlace;
  } | null>(null);
  const pendingConversation = ref("");
  /** Whether entries may be out of date, so their buttons are off. */
  const unavailable = computed(() => unreachableSince.value !== null);

  function apply(next: Customization[]) {
    entries.value = next.map(received);
  }

  async function mutate(
    state: Customization,
    operation: "restart" | "cancelAutoRestart",
    place: CustomizationsPlace,
  ) {
    if (pendingConversation.value) return;
    pendingConversation.value = state.conversation_id;
    requestError.value = null;
    try {
      const next = received(await customizationsApi[operation](state));
      entries.value = [
        ...entries.value.filter((entry) => entry.conversation_id !== next.conversation_id),
        next,
      ];
    } catch (err) {
      requestError.value = {
        message: errorMessage(err),
        conversationId: state.conversation_id,
        place,
      };
    } finally {
      pendingConversation.value = "";
    }
  }

  return { entries, unavailable, requestError, pendingConversation, apply, mutate };
}

const shared = createCustomizationsState();

/** The stream's build states, shared by the drawer and a task's header. */
export function useCustomizations() {
  return shared;
}

export const applyCustomizations = shared.apply;
