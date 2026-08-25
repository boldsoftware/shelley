// absPath.ts — resolve a possibly-relative POSIX path against a base
// directory. Paths recorded by tools are whatever the agent passed, so a patch
// card's path can be relative; the file-reading/writing endpoints require a
// clean absolute path.

/**
 * An absolute, normalized version of `path`, resolving a relative one against
 * `base`. Returns null when that isn't possible: an empty path, or a relative
 * path with no absolute base to root it in. Callers should stay quiet in that
 * case rather than open something bogus.
 */
export function resolveAbsPath(path: string, base: string | null | undefined): string | null {
  if (!path) return null;
  let abs = path;
  if (!abs.startsWith("/")) {
    if (!base || !base.startsWith("/")) return null;
    abs = base.replace(/\/+$/, "") + "/" + abs;
  }
  // Collapse "."/".."/duplicate separators; ".." at the root is dropped, as in
  // filepath.Clean.
  const out: string[] = [];
  for (const seg of abs.split("/")) {
    if (seg === "" || seg === ".") continue;
    if (seg === "..") out.pop();
    else out.push(seg);
  }
  return "/" + out.join("/");
}
