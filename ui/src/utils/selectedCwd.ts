// The working directory the user last picked for a new conversation. App,
// the composer and the command palette all seed new-conversation defaults
// from it, so they share one key.
const SELECTED_CWD_KEY = "shelley_selected_cwd";

/** The last-picked working directory, or null when none is stored. */
export function storedSelectedCwd(): string | null {
  return localStorage.getItem(SELECTED_CWD_KEY);
}

/** Remembers cwd as the last-picked working directory. */
export function storeSelectedCwd(cwd: string): void {
  localStorage.setItem(SELECTED_CWD_KEY, cwd);
}
