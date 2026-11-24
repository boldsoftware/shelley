// Copies text, falling back to a hidden textarea and execCommand("copy")
// where the async Clipboard API is missing or refused (non-secure contexts,
// some embedded webviews). Resolves to whether the copy took.
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // fall through to exec fallback
  }
  try {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand("copy");
    document.body.removeChild(ta);
    return ok;
  } catch {
    return false;
  }
}

// Copies text still being fetched. Safari copies only within a click's
// gesture, which an await ends, so where ClipboardItem takes a promise the
// clipboard gets the pending text; elsewhere it falls back to copyText.
export async function copyPendingText(text: Promise<string>): Promise<boolean> {
  if (typeof ClipboardItem !== "undefined" && navigator.clipboard?.write) {
    try {
      const blob = text.then((t) => new Blob([t], { type: "text/plain" }));
      await navigator.clipboard.write([new ClipboardItem({ "text/plain": blob })]);
      return true;
    } catch {
      // fall through to copyText
    }
  }
  return copyText(await text);
}
