// Collapse the leading $HOME prefix of a path to `~` for display.
// Returns the original path when no home_dir is known or it doesn't apply.
export function tildifyPath(p: string | null | undefined): string | null {
  if (!p) return null;
  const homeDir = window.__SHELLEY_INIT__?.home_dir;
  if (homeDir && p === homeDir) return "~";
  if (homeDir && p.startsWith(homeDir + "/")) return "~" + p.slice(homeDir.length);
  return p;
}
