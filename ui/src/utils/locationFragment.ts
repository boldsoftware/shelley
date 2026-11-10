export function fragmentForMessage(messageId: string): string {
  return `m-${messageId.replace(/[^a-zA-Z0-9]/g, "").slice(0, 8)}`;
}

export function replaceLocationFragment(fragment: string) {
  const url = new URL(window.location.href);
  url.searchParams.delete("message_id");
  url.hash = fragment;
  history.replaceState(null, "", `${url.pathname}${url.search}${url.hash}`);
}
