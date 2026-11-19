// Click-to-view for local markdown files.
//
// Links like [name](sandbox:<path>) to files other than images render as
// per-message download links (utils/markdownRender.ts). A plain click on one
// that names a .md file opens the file rendered in MarkdownDocModal, which
// ChatInterface hosts once, like the image annotation view; this module holds
// the one open document.
// Modifier-clicks keep their native meaning (download).
import { readonly, ref, type Ref } from "vue";
import { isOpenInNewTabClick } from "../utils/openInNewTab";

export interface MarkdownDoc {
  /** The path as linked, for the title. */
  path: string;
  /** The download URL, which also serves the text. */
  url: string;
}

const doc = ref<MarkdownDoc | null>(null);

export function useMarkdownDoc(): Readonly<Ref<MarkdownDoc | null>> {
  return readonly(doc);
}

export function closeMarkdownDoc(): void {
  doc.value = null;
}

/** Click handler for a download link; opens .md files in the viewer. */
export function handleMarkdownDocClick(e: MouseEvent, a: HTMLAnchorElement): void {
  if (isOpenInNewTabClick(e) || e.altKey) return;
  const path = new URL(a.href).searchParams.get("path") ?? "";
  if (!/\.(md|markdown)$/i.test(path)) return;
  e.preventDefault();
  doc.value = { path, url: a.href };
}
