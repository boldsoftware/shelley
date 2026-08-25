// Reactive conversation visibility setting shared by the menu and chat renderer.
import { ref } from "vue";
import {
  getConversationViewMode,
  setConversationViewMode as persist,
  type ConversationViewMode,
} from "../../services/settings";

const mode = ref<ConversationViewMode>(getConversationViewMode());

export function useConversationView() {
  return {
    conversationViewMode: mode,
    setConversationViewMode(next: ConversationViewMode) {
      persist(next);
      mode.value = next;
    },
  };
}

export type { ConversationViewMode };
