// Vue port of components/useEscapeClose.ts. Closes a modal/overlay when Escape
// is pressed while `isOpen` is true. Pass reactive getters.
import { watch, onUnmounted, type Ref } from "vue";

export function useEscapeClose(isOpen: Ref<boolean> | (() => boolean), onClose: () => void) {
  const get = typeof isOpen === "function" ? isOpen : () => isOpen.value;
  let handler: ((e: KeyboardEvent) => void) | null = null;

  const detach = () => {
    if (handler) {
      document.removeEventListener("keydown", handler);
      handler = null;
    }
  };

  watch(
    get,
    (open) => {
      detach();
      if (open) {
        handler = (e: KeyboardEvent) => {
          if (e.key === "Escape") onClose();
        };
        document.addEventListener("keydown", handler);
      }
    },
    { immediate: true },
  );

  onUnmounted(detach);
}
