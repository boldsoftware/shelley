import type { CommitIdentity, CommitIdentitySuggestions } from "../services/workspace";

export const NO_COMMIT_IDENTITY: CommitIdentity = { name: "", email: "" };

export function hasCommitIdentity(identity: CommitIdentity): boolean {
  return !!(identity.name.trim() || identity.email.trim());
}

export function sameCommitIdentity(a: CommitIdentity, b: CommitIdentity): boolean {
  return a.name.trim() === b.name.trim() && a.email.trim() === b.email.trim();
}

/** identity with its empty fields filled from the first suggestions. */
export function prefillCommitIdentity(
  identity: CommitIdentity,
  suggestions: CommitIdentitySuggestions,
): CommitIdentity {
  return {
    name: identity.name || suggestions.names[0] || "",
    email: identity.email || suggestions.emails[0]?.email || "",
  };
}

/** How saving a new project committing as chosen goes, given the default
 * identity: chosen becomes the default if there is none; else a different
 * one is the project's own. */
export function newProjectCommitIdentity(
  defaultIdentity: CommitIdentity,
  chosen: CommitIdentity,
): { setDefault: boolean; project: CommitIdentity } {
  if (!hasCommitIdentity(defaultIdentity)) return { setDefault: true, project: NO_COMMIT_IDENTITY };
  if (sameCommitIdentity(defaultIdentity, chosen))
    return { setDefault: false, project: NO_COMMIT_IDENTITY };
  return { setDefault: false, project: chosen };
}
