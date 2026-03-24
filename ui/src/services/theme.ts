export type ThemeMode = "system" | "light" | "dark";

const STORAGE_KEY = "shelley-theme";

export function getStoredTheme(): ThemeMode {
  const stored = localStorage.getItem(STORAGE_KEY);
  if (stored === "light" || stored === "dark" || stored === "system") {
    return stored;
  }
  return "system";
}

export function setStoredTheme(theme: ThemeMode): void {
  localStorage.setItem(STORAGE_KEY, theme);
}

export function getSystemPrefersDark(): boolean {
  return window.matchMedia("(prefers-color-scheme: dark)").matches;
}

export function isDarkModeActive(): boolean {
  const theme = getStoredTheme();
  return theme === "dark" || (theme === "system" && getSystemPrefersDark());
}

export function applyTheme(theme: ThemeMode): void {
  const isDark = theme === "dark" || (theme === "system" && getSystemPrefersDark());
  document.documentElement.classList.toggle("dark", isDark);
}

// Initialize theme on load
export function initializeTheme(): void {
  const theme = getStoredTheme();
  applyTheme(theme);

  // Listen for system preference changes
  window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
    const currentTheme = getStoredTheme();
    if (currentTheme === "system") {
      applyTheme("system");
    }
  });
}
