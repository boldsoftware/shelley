// The drawer's folded groups, kept across reloads. Storage that fails or holds
// something unexpected reads as nothing folded; failing writes are dropped.
const PROJECT_FOLDS_KEY = "shelley-project-folds";
const COLLAPSED_GROUPS_KEY = "shelley-collapsed-groups";

function read(storage: Storage, key: string): unknown {
  try {
    return JSON.parse(storage.getItem(key) ?? "null");
  } catch {
    return null;
  }
}

function write(storage: Storage, key: string, value: unknown): void {
  try {
    storage.setItem(key, JSON.stringify(value));
  } catch {
    // Best effort; losing the folds is fine.
  }
}

/** Explicit fold choices by project group id (true = folded). */
export function loadProjectFolds(storage: Storage): Map<string, boolean> {
  const stored = read(storage, PROJECT_FOLDS_KEY);
  if (typeof stored !== "object" || stored === null || Array.isArray(stored)) return new Map();
  return new Map(
    Object.entries(stored).filter(
      (entry): entry is [string, boolean] => typeof entry[1] === "boolean",
    ),
  );
}

export function saveProjectFolds(folds: Map<string, boolean>, storage: Storage): void {
  write(storage, PROJECT_FOLDS_KEY, Object.fromEntries(folds));
}

/** The folded groups of the other groupings (by directory, tag, …), by key. */
export function loadCollapsedGroups(storage: Storage): Set<string> {
  const stored = read(storage, COLLAPSED_GROUPS_KEY);
  return new Set(Array.isArray(stored) ? stored.filter((key) => typeof key === "string") : []);
}

export function saveCollapsedGroups(groups: Set<string>, storage: Storage): void {
  write(storage, COLLAPSED_GROUPS_KEY, [...groups]);
}
