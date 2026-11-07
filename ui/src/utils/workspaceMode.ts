// Workspace mode (the server's "workspace" feature flag): a pinned Assistant
// at / routes work into projects, and conversations are called tasks. Read
// once per page load; turning the flag on or off changes the UI build, which
// reloads open pages.
export const workspaceMode =
  typeof window !== "undefined" && window.__SHELLEY_INIT__?.workspace === true;

// The sidebar's conversation rows show only their title (the server's
// "compact-nav" feature flag, on by default in workspace mode). Read once per
// page load.
export const compactNav =
  typeof window !== "undefined" && window.__SHELLEY_INIT__?.compact_nav === true;

// Picks the workspace mode wording over stock Shelley's.
export function wording(stock: string, workspace: string): string {
  return workspaceMode ? workspace : stock;
}
