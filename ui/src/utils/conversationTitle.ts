import { workspaceMode } from "./workspaceMode";

// Human-friendly rendering of a conversation slug for display only:
// "remove-the-required-general-category" -> "Remove the required general category".
export function humanizeSlug(slug: string): string {
  const text = slug.replace(/-/g, " ");
  return text.charAt(0).toUpperCase() + text.slice(1);
}

// A conversation's display title: humanized in workspace mode, else its slug.
export function conversationTitle(slug: string): string {
  return workspaceMode ? humanizeSlug(slug) : slug;
}
