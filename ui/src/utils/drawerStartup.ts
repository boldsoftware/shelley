const COLLAPSED_KEY = "shelley-drawer-collapsed";

// initialDrawerCollapsed decides the drawer's startup state. The drawer starts
// expanded unless the user's last manual toggle collapsed it.
export function initialDrawerCollapsed(storage: Storage): boolean {
  try {
    return storage.getItem(COLLAPSED_KEY) === "true";
  } catch {
    // Storage unavailable (e.g. disabled); start expanded.
    return false;
  }
}

export function saveDrawerCollapsedPreference(collapsed: boolean, storage: Storage): void {
  try {
    storage.setItem(COLLAPSED_KEY, String(collapsed));
  } catch {
    // Best effort; losing the preference is fine.
  }
}
