// The javascript: URL users drag to their bookmarks bar: the page script
// (page.ts, as built) wrapped with where Shelley lives.
export function bookmarkletHref(source: string, origin: string): string {
  const settings = JSON.stringify({ origin });
  return `javascript:${encodeURIComponent(`(function(__shelleyBookmarklet){${source}})(${settings});void 0`)}`;
}
