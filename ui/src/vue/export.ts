// Extract the /export/<id> conversation id from the current path.
export function exportConversationIdFromPath(): string | null {
  const m = window.location.pathname.match(/^\/export\/([^/]+)\/?$/);
  return m ? decodeURIComponent(m[1]) : null;
}
