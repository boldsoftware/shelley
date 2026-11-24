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
// whichever is newer. The arbiter is the row's `draft_updated_at`, which the
// server stamps whenever the text changes (a PUT /draft, or a send clearing
// it). Each keystroke we record, alongside the cached text, the
// `draft_updated_at` the composer was last in sync with (`basedOn`). On load
// the local copy wins iff its `basedOn` is >= the server's current
// `draft_updated_at` AND its text differs — i.e. the user typed past what the
// server has acknowledged. This naturally covers the lost-connection case:
// failed PUTs never advance the stamp, so `basedOn` stays equal to it and the
// locally-typed text is preserved.
//
// We never try to flush localStorage back to the server; on the next keystroke
// the normal autosave carries the merged text forward and the two converge.
//
// Every conversation, draft or started, has a server copy of its composer
// text, so it follows the user between browsers and devices. Only the
// new-conversation view has none until its draft is created: its entry
// (basedOn "") always wins.

const PREFIX = "shelley-draft:";

// localStorage key for a draft session. `null` is the special "new
// conversation" session (no server id yet); a lazily-created draft migrates
// its cache to the real id (see ChatInterface).
function cacheKey(id: string | null): string {
  return PREFIX + (id ?? "new");
}

export interface CachedDraft {
  value: string;
  // The server row's `draft_updated_at` the composer was last reconciled with
  // when this cache entry was written. Empty string for the new-conversation
  // session (no server row yet), and for a conversation whose text was never
  // saved; such an entry wins on load against a row with no saved text.
  basedOn: string;
  // Set while the text is being POSTed as a message. The entry is shared by
  // every tab on the conversation, and the new message's row echo can reach
  // another tab before the POST returns to the sender; that tab's echo-driven
  // reconcile must not seed the departing text into its own composer. The
  // sending tab keeps seeing it as the composer's own text, and a fresh entry
  // into the session (a reload mid-send) still restores it, so a send the
  // server never received is not lost.
  pending?: boolean;
}

export function loadCachedDraft(id: string | null): CachedDraft | null {
  try {
    const raw = localStorage.getItem(cacheKey(id));
    if (raw === null) return null;
    const parsed = JSON.parse(raw);
    if (typeof parsed?.value !== "string" || typeof parsed?.basedOn !== "string") {
      return null;
    }
    return { value: parsed.value, basedOn: parsed.basedOn, pending: parsed.pending === true };
  } catch {
    return null;
  }
}

export function saveCachedDraft(
  id: string | null,
  value: string,
  basedOn: string,
  pending = false,
): void {
  try {
    localStorage.setItem(
      cacheKey(id),
      JSON.stringify({ value, basedOn, pending: pending || undefined }),
    );
  } catch {
    // Quota or disabled storage: nothing we can do; server autosave remains.
  }
}

// compareStamps orders two server stamps (RFC 3339; "" for none, the oldest).
// Not as strings: the server trims trailing zeros off their fractional
// seconds, and "…:05.1Z" sorts before "…:05Z" as a string.
export function compareStamps(a: string, b: string): number {
  const ka = stampKey(a);
  const kb = stampKey(b);
  return ka < kb ? -1 : ka > kb ? 1 : 0;
}

function stampKey(stamp: string): string {
  if (!stamp) return "";
  const utc = /^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d+))?Z$/.exec(stamp);
  if (utc) return `${utc[1]}.${(utc[2] ?? "").padEnd(9, "0")}`;
  const time = new Date(stamp);
  return isNaN(time.getTime()) ? stamp : stampKey(time.toISOString());
}

// rebaseCachedDraft advances the entry's `basedOn` after the server acknowledged
// a PUT with a newer `draft_updated_at`, so keystrokes typed while that PUT was
// outstanding (stamped with the older time) stay ahead of the server. Only
// advances: responses can land out of order, and regressing the stamp would
// re-open the stale-cache window.
export function rebaseCachedDraft(id: string, updatedAt: string): void {
  const cur = loadCachedDraft(id);
  if (cur && compareStamps(updatedAt, cur.basedOn) > 0)
    saveCachedDraft(id, cur.value, updatedAt, cur.pending);
}

// markCachedDraftPending flags the entry holding `text` as in flight (see
// CachedDraft.pending). Call it BEFORE the chat POST goes out. Returns an undo
// for the failure path, which unflags the entry unless it has since been
// rewritten with other text.
export function markCachedDraftPending(id: string, text: string): () => void {
  const cur = loadCachedDraft(id);
  if (!cur || cur.value !== text) return () => {};
  saveCachedDraft(id, cur.value, cur.basedOn, true);
  return () => {
    const now = loadCachedDraft(id);
    if (now?.pending && now.value === text) saveCachedDraft(id, now.value, now.basedOn);
  };
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
  // The server row's `draft_updated_at`. Empty string when there is no server
  // row yet (new-conversation view) or it never saved text.
  updatedAt: string;
}

// pickDraft chooses between the server's copy and the locally-cached copy.
//
// The local copy wins only when the user has typed past what the server has
// acknowledged: its text differs from the server's AND it was based on a
// server state at least as recent as the server's current `draft_updated_at`. The
// >= comparison (rather than >) is deliberate: a dropped connection leaves
// `basedOn` exactly equal to the frozen server `updated_at`, yet the local
// text is the one we must keep. When the server's `updated_at` is strictly
// newer than `basedOn`, the server has changes the cache predates (e.g. an
// edit from another device), so the server wins.
export function pickDraft(server: DraftCandidate, local: CachedDraft | null): DraftCandidate {
  if (
    local &&
    local.value !== server.value &&
    compareStamps(local.basedOn, server.updatedAt) >= 0
  ) {
    return { value: local.value, updatedAt: server.updatedAt };
  }
  return server;
}

// reconcileComposerDraft decides what (if anything) the message composer should
// be (re)seeded with when the focused conversation, its draft text, or its
// `draft_updated_at` changes, and whether new messages clear it. It is the pure core of ChatInterface's draft
// reconcile watch, extracted so the ordering-sensitive logic can be unit tested.
//
// The bug this guards against: on a slow network the autosave round-trip (PUT
// /draft) and the conversation-row echo that streams the new stamp/draft
// back can arrive out of order. In that window the localStorage mirror's
// `basedOn` still points at the *previous* server state, so pickDraft() looks
// stale and returns the OLDER server snapshot. If we blindly wrote that into the
// composer while the user was mid-keystroke, their text got rewritten and the
// caret jumped to the end (reported on Safari over a high-latency link).
//
// Fix: a session's composer is seeded once on entry; after that a same-session
// echo may only update the composer when doing so cannot clobber in-progress
// typing — either the candidate equals what's already shown, or the user has
// not edited since our last seed (the composer still holds the seeded value).
export interface ComposerReconcileInput {
  // Focused conversation id; null is the new-conversation view.
  conversationId: string | null;
  // Id of a lazily-created draft for the current input session, if any.
  lazyDraftId: string | null;
  // Whether the focused conversation row is a draft.
  isDraft: boolean;
  // The focused conversation row's server draft text and `draft_updated_at`
  // ("" when it never saved text).
  serverDraft: string;
  serverUpdatedAt: string;
  // The `draft_updated_at` the composer is synced to: it has taken in every
  // version up to it (seeded, saved, or typed over), so an echo of one is
  // stale ("" when none).
  syncedAt?: string;
  // The localStorage mirror for this session (null if none).
  cached: CachedDraft | null;
  // Whether a send from THIS tab flagged `cached` pending (it is then still
  // the composer's own text, not another tab's departing one).
  ownsPending: boolean;
  // The server draft text this echo turned into a message (the row flipped
  // is_draft true->false without changing conversation, and its text went),
  // else null. While the sending tab's POST is in flight its pending entry is
  // the more exact record of what went (the server draft may lag its last
  // keystrokes). A started conversation's composer needs no such case: synced
  // and idle, it follows the server's text (lastSeededValue), cleared or not.
  promotedFrom: string | null;
  // Whether only the conversation's messages or queue changed. That carries
  // no draft, but may be text leaving another tab's composer (its row echo can
  // miss it: updated_at has one-second resolution), so this run may clear such
  // text from this composer and never seeds any.
  messagesOnly?: boolean;
  // The composer's live value right now (latest keystrokes).
  composerValue: string;
  // The session id we last seeded the composer for (undefined before any seed).
  lastSeededSession: string | null | undefined;
  // The value we last programmatically wrote into the composer.
  lastSeededValue: string;
}

export interface ComposerReconcileResult {
  // The value to write into the composer.
  value: string;
  // The server `draft_updated_at` this value is reconciled against ("" when
  // none).
  draftSyncedAt: string;
  // The session id to record as seeded.
  seededSession: string | null;
}

export function reconcileComposerDraft(
  input: ComposerReconcileInput,
): ComposerReconcileResult | null {
  const {
    conversationId,
    lazyDraftId,
    isDraft,
    serverDraft,
    serverUpdatedAt,
    ownsPending,
    promotedFrom,
    composerValue,
    lastSeededSession,
    lastSeededValue,
  } = input;

  const sessionId = conversationId; // null == new-conversation view

  // A brand-new conversation auto-saving a draft flips conversationId
  // null->draftId mid-typing. That is the same input session, not a switch, so
  // leave the composer (and the user's keystrokes) untouched. Record the
  // session on the flip, though: the echoes that follow (the promotion, above
  // all) are same-session echoes, not a fresh entry.
  if (conversationId !== null && conversationId === lazyDraftId && isDraft) {
    if (lastSeededSession === sessionId) return null;
    return { value: composerValue, draftSyncedAt: serverUpdatedAt, seededSession: sessionId };
  }

  // A pending entry is text on its way OUT of a composer (some tab's send is in
  // flight). A same-session echo must not carry it into this one — unless the
  // send is ours, in which case it IS this composer's text and the echo must
  // not disturb it. A fresh entry into the session (reload mid-send) restores
  // it regardless.
  const foreignPending = !!input.cached?.pending && !ownsPending;
  const cached = lastSeededSession === sessionId && foreignPending ? null : input.cached;

  // Compute the candidate value + sync stamp for this session. The
  // new-conversation view has no server copy.
  const server =
    conversationId === null
      ? { value: "", updatedAt: "" }
      : { value: serverDraft, updatedAt: serverUpdatedAt };
  // This tab's own send in flight: the entry is still the composer's text,
  // which must outlive an echo of the server clearing it, in case the POST
  // fails yet.
  const own = ownsPending && cached?.pending;
  const value = own ? cached.value : pickDraft(server, cached).value;
  const draftSyncedAt = server.updatedAt;

  // First entry into a session always seeds.
  if (lastSeededSession !== sessionId) {
    return { value, draftSyncedAt, seededSession: sessionId };
  }

  // This tab's send from the composer is in flight: the composer keeps the
  // text it sent until the POST settles; text another device saved meanwhile
  // shows after.
  if (ownsPending) return null;

  // Same session: this is a server echo. Applying it must not clobber
  // in-progress keystrokes. Safe only when the candidate is already what the
  // composer shows, or the user hasn't edited since our last seed. One more
  // case: the draft was just sent from another tab (this tab's own send keeps
  // its composer until the POST returns, and its own pending entry is still
  // its text) and what went contains the composer's text: verbatim, or with
  // the other tab's last-second additions around it. That text is done with,
  // even though this tab typed it; otherwise the sent text would linger as a
  // stale next-message draft. Text the other tab replaced, or that this tab
  // typed beyond what went, was never sent and stays.
  if (value === composerValue) return null;
  if (input.messagesOnly && value !== "") return null;
  // A late echo, say of this tab's save of text it has sent since.
  if (
    !input.messagesOnly &&
    conversationId !== null &&
    compareStamps(serverUpdatedAt, input.syncedAt ?? "") <= 0
  ) {
    return null;
  }
  let sentText = promotedFrom;
  if (sentText !== null && foreignPending && input.cached) sentText = input.cached.value;
  const sentAsTyped = sentText !== null && composerValue !== "" && sentText.includes(composerValue);
  if (composerValue === lastSeededValue || sentAsTyped) {
    return { value, draftSyncedAt, seededSession: sessionId };
  }
  return null;
}
