// Vue/native-event port of utils/openInNewTab.ts (the React version types its
// param as React.MouseEvent; here we use the DOM MouseEvent).

/** True if a mouse event has a modifier indicating "open in new tab". */
export function isOpenInNewTabClick(e: MouseEvent): boolean {
  return e.metaKey || e.ctrlKey || e.shiftKey;
}

/** If the click has a "new tab" modifier, open `url` in a new tab and return
 *  true; otherwise return false so the caller does its normal SPA action. */
export function handleModifiedNavClick(e: MouseEvent, url: string): boolean {
  if (!isOpenInNewTabClick(e)) return false;
  e.preventDefault();
  e.stopPropagation();
  window.open(url, "_blank", "noopener");
  return true;
}
