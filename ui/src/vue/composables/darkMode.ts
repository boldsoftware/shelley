import { onMounted, onUnmounted, ref, type Ref } from "vue";
import { isDarkModeActive } from "../../services/theme";

/** Whether the page is dark, following theme changes while mounted. */
export function useDarkMode(): Ref<boolean> {
  const isDark = ref(isDarkModeActive());
  let observer: MutationObserver | null = null;
  onMounted(() => {
    observer = new MutationObserver(() => {
      isDark.value = isDarkModeActive();
    });
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ["class"] });
  });
  onUnmounted(() => observer?.disconnect());
  return isDark;
}
