import type { CommitInfo } from "../../types";

export function versionChangelogTags(
  isOpen: boolean,
  hasUpdate: boolean | undefined,
  currentTag: string | undefined,
  latestTag: string | undefined,
): readonly [currentTag: string, latestTag: string] | null {
  if (!isOpen || !hasUpdate || !currentTag || !latestTag) return null;
  return [currentTag, latestTag];
}

export type VersionChangelogLoadResult =
  | { ok: true; commits: CommitInfo[] }
  | { ok: false; error: unknown };

export function createVersionChangelogLoader(
  fetchChangelog: (currentTag: string, latestTag: string) => Promise<CommitInfo[]>,
) {
  let generation = 0;

  return {
    invalidate() {
      generation++;
    },
    async load(currentTag: string, latestTag: string): Promise<VersionChangelogLoadResult | null> {
      const requestGeneration = ++generation;
      try {
        const commits = (await fetchChangelog(currentTag, latestTag)) || [];
        if (requestGeneration !== generation) return null;
        return { ok: true, commits };
      } catch (error) {
        if (requestGeneration !== generation) return null;
        return { ok: false, error };
      }
    },
  };
}
