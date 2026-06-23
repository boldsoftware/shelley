// Reactive theme state, backed by services/theme.ts.
// The actual <html class="dark"> toggling stays in services/theme.ts so the
// behavior (and the CSS contract) is identical to the React app.
import { ref } from "vue";
import {
  getStoredTheme,
  setStoredTheme,
  applyTheme,
  isDarkModeActive,
  type ThemeMode,
} from "../../services/theme";

const theme = ref<ThemeMode>(getStoredTheme());
const isDark = ref<boolean>(isDarkModeActive());

// Keep isDark in sync when the system preference changes while on "system".
window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
  if (theme.value === "system") isDark.value = isDarkModeActive();
});

export function useTheme() {
  return {
    theme,
    isDark,
    setTheme(t: ThemeMode) {
      setStoredTheme(t);
      applyTheme(t);
      theme.value = t;
      isDark.value = isDarkModeActive();
    },
  };
}

export type { ThemeMode };
