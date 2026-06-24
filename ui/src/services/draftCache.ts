// draftCache mirrors the message composer's autosave into localStorage so a
// reload (or a silently dropped network connection) can never lose unsent
// draft text.
//
// The server-side draft autosave (PUT /conversation/<id>/draft) is
// best-effort and debounced: there is always a window after a keystroke where
// the text lives only in the browser. If the tab reloads in that window, or
// the connection died without the user noticing, every PUT since the last
// successful one is lost. To plug that hole we additionally persist the draft
// to localStorage on EVERY keystroke (synchronous, no network).
//
// On load we read the draft from the server row and from localStorage and keep
// whichever is newer, WITHOUT any server-side schema change. The arbiter is
// the conversation row's existing `updated_at`, which the server bumps on each
// successful PUT /draft. Each keystroke we record, alongside the cached text,
// the server `updated_at` the composer was last in sync with (`basedOn`). On
// load the local copy wins iff its `basedOn` is >= the server's current
// `updated_at` AND its text differs — i.e. the user typed past what the server
// has acknowledged. This naturally covers the lost-connection case: failed
// PUTs never advance `updated_at`, so `basedOn` stays equal to it and the
// locally-typed text is preserved.
//
// We never try to flush localStorage back to the server; on the next keystroke
// the normal autosave carries the merged text forward and the two converge.

const PREFIX = "shelley-draft:";

// localStorage key for a draft session. `null` is the special "new
// conversation" session (no server id yet); a lazily-created draft migrates
// its cache to the real id (see ChatInterface).
function cacheKey(id: string | null): string {
  return PREFIX + (id ?? "new");
}

export interface CachedDraft {
  value: string;
  // The server row's `updated_at` the composer was last reconciled with when
  // this cache entry was written. Empty string for the new-conversation
  // session (no server row yet); such an entry always wins on load since any
  // server row that later appears is a fresh draft we just created.
  basedOn: string;
}

export function loadCachedDraft(id: string | null): CachedDraft | null {
  try {
    const raw = localStorage.getItem(cacheKey(id));
    if (raw === null) return null;
    const parsed = JSON.parse(raw);
    if (typeof parsed?.value !== "string" || typeof parsed?.basedOn !== "string") {
      return null;
    }
    return { value: parsed.value, basedOn: parsed.basedOn };
  } catch {
    return null;
  }
}

export function saveCachedDraft(id: string | null, value: string, basedOn: string): void {
  try {
    localStorage.setItem(cacheKey(id), JSON.stringify({ value, basedOn }));
  } catch {
    // Quota or disabled storage: nothing we can do; server autosave remains.
  }
}

export function clearCachedDraft(id: string | null): void {
  try {
    localStorage.removeItem(cacheKey(id));
  } catch {
    // ignore
  }
}

export interface DraftCandidate {
  value: string;
  // The server row's `updated_at`. Empty string when there is no server row
  // yet (new-conversation view).
  updatedAt: string;
}

// pickDraft chooses between the server's copy and the locally-cached copy.
//
// The local copy wins only when the user has typed past what the server has
// acknowledged: its text differs from the server's AND it was based on a
// server state at least as recent as the server's current `updated_at`. The
// >= comparison (rather than >) is deliberate: a dropped connection leaves
// `basedOn` exactly equal to the frozen server `updated_at`, yet the local
// text is the one we must keep. When the server's `updated_at` is strictly
// newer than `basedOn`, the server has changes the cache predates (e.g. an
// edit from another tab), so the server wins.
export function pickDraft(server: DraftCandidate, local: CachedDraft | null): DraftCandidate {
  if (
    local &&
    local.value !== server.value &&
    local.basedOn >= server.updatedAt
  ) {
    return { value: local.value, updatedAt: server.updatedAt };
  }
  return server;
}
