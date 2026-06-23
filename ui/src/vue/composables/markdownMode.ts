// Reactive markdown-mode setting, backed by services/settings.ts.
import { ref } from "vue";
import {
  getMarkdownMode,
  setMarkdownMode as persist,
  type MarkdownMode,
} from "../../services/settings";

const mode = ref<MarkdownMode>(getMarkdownMode());

export function useMarkdownMode() {
  return {
    markdownMode: mode,
    setMarkdownMode(m: MarkdownMode) {
      persist(m);
      mode.value = m;
    },
  };
}

export type { MarkdownMode };
