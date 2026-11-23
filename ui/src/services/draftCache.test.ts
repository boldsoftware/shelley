// draftCache tests — localStorage mirror of the composer autosave.
//
// Run via `pnpm test` (see scripts/run-tests.mjs).

import {
  loadCachedDraft,
  saveCachedDraft,
  clearCachedDraft,
  rebaseCachedDraft,
  markCachedDraftPending,
  pickDraft,
  keepDraftThroughPromotion,
  reconcileComposerDraft,
  type ComposerReconcileInput,
} from "./draftCache";

function assert(cond: boolean, msg: string): void {
  if (!cond) throw new Error(`Assertion failed: ${msg}`);
}
function run(name: string, fn: () => void): void {
  try {
    fn();
    console.log(`\u2713 ${name}`);
  } catch (err) {
    console.error(`\u2717 ${name}`);
    throw err;
  }
}

// Minimal in-memory localStorage polyfill for Node.
function installLocalStorage(): void {
  const store = new Map<string, string>();
  const ls = {
    getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
    setItem: (k: string, v: string) => void store.set(k, String(v)),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
    key: (i: number) => Array.from(store.keys())[i] ?? null,
    get length() {
      return store.size;
    },
  };
  Object.defineProperty(globalThis, "localStorage", { value: ls, configurable: true });
}

installLocalStorage();

run("round-trips a cached draft by id", () => {
  saveCachedDraft("c123", "hello", "2026-01-01T00:00:05Z");
  const got = loadCachedDraft("c123");
  assert(got?.value === "hello" && got?.basedOn === "2026-01-01T00:00:05Z", "loads what was saved");
});

run("uses a distinct slot for the new-conversation session", () => {
  saveCachedDraft(null, "new draft", "");
  saveCachedDraft("c1", "existing", "2026-01-01T00:00:09Z");
  assert(loadCachedDraft(null)?.value === "new draft", "null slot isolated");
  assert(loadCachedDraft("c1")?.value === "existing", "id slot isolated");
});

run("returns null for an absent or malformed entry", () => {
  assert(loadCachedDraft("missing") === null, "absent → null");
  localStorage.setItem("shelley-draft:bad", "{not json");
  assert(loadCachedDraft("bad") === null, "malformed → null");
});

run("clearCachedDraft removes the entry", () => {
  saveCachedDraft("c9", "x", "");
  clearCachedDraft("c9");
  assert(loadCachedDraft("c9") === null, "cleared → null");
});

run("rebaseCachedDraft only advances the stamp and keeps the pending flag", () => {
  saveCachedDraft("c10", "x", "2026-01-01T00:00:05Z", true);
  rebaseCachedDraft("c10", "2026-01-01T00:00:03Z");
  assert(loadCachedDraft("c10")?.basedOn === "2026-01-01T00:00:05Z", "older stamp ignored");
  rebaseCachedDraft("c10", "2026-01-01T00:00:09Z");
  const got = loadCachedDraft("c10");
  assert(got?.basedOn === "2026-01-01T00:00:09Z", "newer stamp applied");
  assert(got?.pending === true, "pending survives a re-base");
});

run("markCachedDraftPending flags the submitted text and its undo unflags it", () => {
  saveCachedDraft("c11", "sending", "t1");
  const undo = markCachedDraftPending("c11", "sending");
  assert(loadCachedDraft("c11")?.pending === true, "flagged");
  // A PUT ack that lands mid-send re-bases the entry; the undo keeps that.
  rebaseCachedDraft("c11", "t2");
  undo();
  const got = loadCachedDraft("c11");
  assert(got?.pending === false && got.basedOn === "t2", "unflagged, newer stamp kept");
});

run("markCachedDraftPending leaves other text alone", () => {
  saveCachedDraft("c12", "edited since", "");
  markCachedDraftPending("c12", "submitted");
  assert(loadCachedDraft("c12")?.pending === false, "mismatch not flagged");
  saveCachedDraft("c12", "submitted", "");
  const undo = markCachedDraftPending("c12", "submitted");
  // Another tab's send of newer text is now in flight; our undo must not unflag it.
  saveCachedDraft("c12", "typed more", "", true);
  undo();
  const got = loadCachedDraft("c12");
  assert(got?.value === "typed more" && got.pending === true, "undo does not touch newer text");
});

run("pickDraft keeps local edits the server never acknowledged", () => {
  // Connection dropped: server's updated_at is frozen at t5; the user kept
  // typing, so the cache was stamped with that same t5 but holds newer text.
  const server = { value: "saved at t5", updatedAt: "2026-01-01T00:00:05Z" };
  const local = { value: "typed after t5", basedOn: "2026-01-01T00:00:05Z" };
  assert(pickDraft(server, local).value === "typed after t5", "unacked local wins");
});

run("pickDraft defers to a server copy that advanced past the cache", () => {
  // Another tab saved at t9; our cache predates it (based on t5).
  const server = { value: "newer from other tab", updatedAt: "2026-01-01T00:00:09Z" };
  const local = { value: "my stale text", basedOn: "2026-01-01T00:00:05Z" };
  assert(pickDraft(server, local).value === "newer from other tab", "newer server wins");
});

run("pickDraft defers to the server when text matches", () => {
  const server = { value: "same", updatedAt: "2026-01-01T00:00:05Z" };
  const local = { value: "same", basedOn: "2026-01-01T00:00:05Z" };
  assert(pickDraft(server, local).value === "same", "equal text → server (no-op)");
});

run("pickDraft defers to the server when there is no cache", () => {
  const server = { value: "server", updatedAt: "2026-01-01T00:00:05Z" };
  assert(pickDraft(server, null).value === "server", "no local → server");
});

run("pickDraft keeps a brand-new-view local draft (empty basedOn)", () => {
  // New-conversation view: no server row yet, so updatedAt is "" and the
  // local entry's basedOn is ""; the local text must survive a reload.
  const server = { value: "", updatedAt: "" };
  const local = { value: "composing something", basedOn: "" };
  assert(pickDraft(server, local).value === "composing something", "new-view local wins");
});

// --- reconcileComposerDraft: the ChatInterface reconcile watch's pure core ---
// These pin the fix for the Safari "cursor jumps to end / text rewritten as I
// type" bug: on a slow network the autosave PUT-ack and the conversation-row
// echo arrive out of order, so a same-session echo must never overwrite the
// composer while the user is mid-keystroke.

function reconcileInput(over: Partial<ComposerReconcileInput>): ComposerReconcileInput {
  return {
    conversationId: "c1",
    lazyDraftId: null,
    isDraft: true,
    serverDraft: "",
    serverUpdatedAt: "2026-01-01T00:00:05Z",
    cached: null,
    ownsPending: false,
    promotedFrom: null,
    composerValue: "",
    lastSeededSession: undefined,
    lastSeededValue: "",
    ...over,
  };
}

run("reconcile seeds the composer on first entry into a session", () => {
  const r = reconcileComposerDraft(
    reconcileInput({ serverDraft: "hello from server", lastSeededSession: undefined }),
  );
  assert(r !== null && r.value === "hello from server", "first entry seeds");
  assert(r!.seededSession === "c1", "records seeded session");
});

run("reconcile leaves the composer untouched during a lazy-draft flip", () => {
  // conversationId flipped null->draftId for the same input session: the
  // composer keeps its keystrokes, and the session is recorded as seeded so the
  // echoes that follow count as same-session ones.
  const flip = reconcileComposerDraft(
    reconcileInput({
      conversationId: "draft9",
      lazyDraftId: "draft9",
      composerValue: "typing",
      lastSeededSession: null,
    }),
  );
  assert(
    flip !== null &&
      flip.value === "typing" &&
      flip.seededSession === "draft9" &&
      flip.draftSyncedAt === "2026-01-01T00:00:05Z",
    "lazy-draft flip records the session without touching the text",
  );
  // Later echoes while the lazy draft is still a draft stay hands-off.
  const echo = reconcileComposerDraft(
    reconcileInput({
      conversationId: "draft9",
      lazyDraftId: "draft9",
      serverDraft: "typ",
      composerValue: "typing more",
      lastSeededSession: "draft9",
      lastSeededValue: "typing",
    }),
  );
  assert(echo === null, "lazy-draft echo is a no-op");
});

run("reconcile clears a typed composer when another tab sends the draft", () => {
  // This tab typed the draft (lazily created, so lastSeededValue is what the
  // flip recorded, not the final text); tab B opened it and sent it, and its
  // POST is still in flight (pending entry). The promotion echo must clear the
  // sent text here even though the typing guard would normally keep it.
  const r = reconcileComposerDraft(
    reconcileInput({
      conversationId: "draft9",
      lazyDraftId: "draft9",
      isDraft: false,
      serverDraft: "",
      cached: { value: "the draft", basedOn: "2026-01-01T00:00:05Z", pending: true },
      promotedFrom: "the draft",
      composerValue: "the draft",
      lastSeededSession: "draft9",
      lastSeededValue: "the",
    }),
  );
  assert(
    r !== null && r.value === "" && r.seededSession === "draft9",
    "promotion clears the composer",
  );
  // Same, once tab B's POST has returned and cleared the mirror.
  const later = reconcileComposerDraft(
    reconcileInput({
      conversationId: "d2",
      isDraft: false,
      promotedFrom: "the draft",
      composerValue: "the draft",
      lastSeededSession: "d2",
      lastSeededValue: "",
    }),
  );
  assert(later !== null && later.value === "", "promotion clears the composer (mirror gone)");
});

run("reconcile keeps a typed composer the promotion did not match", () => {
  // The user kept typing after the text the server had; another tab sent that
  // older text. Their newer keystrokes are not what was sent: keep them.
  const r = reconcileComposerDraft(
    reconcileInput({
      isDraft: false,
      promotedFrom: "the dra",
      composerValue: "the draft plus",
      lastSeededSession: "c1",
      lastSeededValue: "",
    }),
  );
  assert(r === null, "unmatched promotion leaves live keystrokes alone");
  // Tab B replaced the draft with other text and sent it before its autosave
  // landed: the server still had this tab's text, but B's pending entry says
  // what really went. This tab's text was never sent: keep it.
  const replaced = reconcileComposerDraft(
    reconcileInput({
      isDraft: false,
      cached: { value: "other", basedOn: "", pending: true },
      promotedFrom: "mine",
      composerValue: "mine",
      lastSeededSession: "c1",
      lastSeededValue: "",
    }),
  );
  assert(replaced === null, "text another tab replaced before sending is kept");
  // Tab B added to the draft and sent at once (before its autosave): what went
  // contains this tab's text, so this tab's copy is done with too.
  const extended = reconcileComposerDraft(
    reconcileInput({
      isDraft: false,
      cached: { value: "mine, and more", basedOn: "", pending: true },
      promotedFrom: "mine",
      composerValue: "mine",
      lastSeededSession: "c1",
      lastSeededValue: "",
    }),
  );
  assert(
    extended !== null && extended.value === "",
    "text another tab extended before sending is cleared",
  );
});

run("reconcile leaves the sending tab's composer alone on its own promotion", () => {
  // This tab sent the draft; its composer keeps the text until the POST returns
  // (MessageInput clears it on success). The promotion echo must not blank it.
  const r = reconcileComposerDraft(
    reconcileInput({
      isDraft: false,
      cached: { value: "the draft", basedOn: "", pending: true },
      ownsPending: true,
      promotedFrom: "the draft",
      composerValue: "the draft",
      lastSeededSession: "c1",
      lastSeededValue: "the draft",
    }),
  );
  assert(r === null, "own promotion echo is a no-op");
});

run("reconcile does NOT clobber in-progress typing on a stale server echo", () => {
  // The user is mid-keystroke ("my new text"); a delayed echo carries an OLDER
  // server snapshot (out-of-order autosave over a slow link). Applying it would
  // rewrite the textarea and jump the caret to the end — the reported bug.
  const r = reconcileComposerDraft(
    reconcileInput({
      serverDraft: "stale server snapshot",
      composerValue: "my new text",
      lastSeededSession: "c1",
      lastSeededValue: "my ne",
    }),
  );
  assert(r === null, "stale echo must not overwrite live keystrokes");
});

run("reconcile applies a same-session echo when the composer is untouched", () => {
  // No local edits since our last seed (composer still holds the seeded value):
  // a server-driven change (e.g. edit from another tab) may safely apply.
  const r = reconcileComposerDraft(
    reconcileInput({
      serverDraft: "updated from another tab",
      serverUpdatedAt: "2026-01-01T00:00:09Z",
      composerValue: "seeded",
      lastSeededSession: "c1",
      lastSeededValue: "seeded",
    }),
  );
  assert(r !== null && r.value === "updated from another tab", "untouched composer accepts echo");
});

run("reconcile is a no-op when the echo already matches the composer", () => {
  const r = reconcileComposerDraft(
    reconcileInput({
      serverDraft: "same text",
      composerValue: "same text",
      lastSeededSession: "c1",
      lastSeededValue: "different-seed",
    }),
  );
  assert(r === null, "echo equal to composer is a no-op");
});

run("reconcile prefers unacked local keystrokes when first seeding a draft", () => {
  // Reload after a dropped connection: server frozen at t5, cache stamped t5
  // with newer text. First seed must restore the user's text, not the server's.
  const r = reconcileComposerDraft(
    reconcileInput({
      serverDraft: "saved at t5",
      serverUpdatedAt: "2026-01-01T00:00:05Z",
      cached: { value: "typed after t5", basedOn: "2026-01-01T00:00:05Z" },
      lastSeededSession: undefined,
    }),
  );
  assert(r !== null && r.value === "typed after t5", "unacked local restored on seed");
});

run("reconcile seeds the new-conversation view from its local mirror", () => {
  const r = reconcileComposerDraft(
    reconcileInput({
      conversationId: null,
      isDraft: false,
      cached: { value: "unsent new draft", basedOn: "" },
      lastSeededSession: undefined,
    }),
  );
  assert(r !== null && r.value === "unsent new draft", "new-view seeds from cache");
  assert(r!.seededSession === null, "new-view session id is null");
});

run("reconcile seeds a non-draft conversation from its authoritative cache once", () => {
  const first = reconcileComposerDraft(
    reconcileInput({
      conversationId: "sent1",
      isDraft: false,
      cached: { value: "next message", basedOn: "" },
      lastSeededSession: undefined,
    }),
  );
  assert(first !== null && first.value === "next message", "non-draft first entry seeds");
  // A later updated_at echo (new agent message) must not wipe the edit.
  const echo = reconcileComposerDraft(
    reconcileInput({
      conversationId: "sent1",
      isDraft: false,
      cached: { value: "next message", basedOn: "" },
      composerValue: "next message and more",
      lastSeededSession: "sent1",
      lastSeededValue: "next message",
    }),
  );
  assert(echo === null, "non-draft echo does not clobber edits");
});

run("keeps the newer draft text through promotion", () => {
  const server = { conversation_id: "p1", draft: "from elsewhere", updated_at: "t2" };
  keepDraftThroughPromotion(server);
  assert(loadCachedDraft("p1")?.value === "from elsewhere", "server text is kept");
  saveCachedDraft("p2", "typed here", "t2");
  keepDraftThroughPromotion({ ...server, conversation_id: "p2" });
  assert(loadCachedDraft("p2")?.value === "typed here", "unsynced local text wins");
  saveCachedDraft("p3", "stale", "t1");
  keepDraftThroughPromotion({ ...server, conversation_id: "p3" });
  assert(loadCachedDraft("p3")?.value === "from elsewhere", "stale local text loses");
  keepDraftThroughPromotion({ conversation_id: "p4", draft: "", updated_at: "t2" });
  assert(loadCachedDraft("p4") === null, "nothing to keep");
  saveCachedDraft("p5", "cleared elsewhere", "t1");
  keepDraftThroughPromotion({ conversation_id: "p5", draft: "", updated_at: "t2" });
  assert(loadCachedDraft("p5") === null, "text cleared elsewhere stays cleared");
});

run("reconcile ignores a pending entry on a same-session echo", () => {
  // Tab B is sending "leaving"; its row echo reaches this tab first. The
  // shared entry is flagged pending, so an untouched composer here stays
  // empty rather than picking up the departing text...
  const pending = { value: "leaving", basedOn: "", pending: true };
  const echo = reconcileComposerDraft(
    reconcileInput({
      conversationId: "sent1",
      isDraft: false,
      cached: pending,
      lastSeededSession: "sent1",
    }),
  );
  assert(echo === null, "pending entry does not seed on echo");
  // ...and one that mirrored the text from a server echo (a draft promoted
  // from the other tab) is cleared.
  const promoted = reconcileComposerDraft(
    reconcileInput({
      conversationId: "sent1",
      isDraft: false,
      cached: pending,
      composerValue: "leaving",
      lastSeededSession: "sent1",
      lastSeededValue: "leaving",
    }),
  );
  assert(promoted !== null && promoted.value === "", "promotion echo clears the composer");
});

run("reconcile on new messages alone clears text sent elsewhere, never seeds", () => {
  // This composer picked up tab B's unsent "picked up" from an echo. B sent it
  // in the same second, so only the message (or queued one) shows up here.
  const picked = {
    conversationId: "sent2",
    isDraft: false,
    composerValue: "picked up",
    lastSeededSession: "sent2",
    lastSeededValue: "picked up",
    messagesOnly: true,
  };
  for (const cached of [{ value: "picked up", basedOn: "", pending: true }, null]) {
    const r = reconcileComposerDraft(reconcileInput({ ...picked, cached }));
    assert(r !== null && r.value === "", "sent text leaves, in flight or accepted");
  }
  // What B has typed since is no message: it waits for a row echo.
  const typed = reconcileComposerDraft(
    reconcileInput({ ...picked, cached: { value: "picked up, and more", basedOn: "" } }),
  );
  assert(typed === null, "new mirror text is not seeded");
  // Nor is text typed here cleared.
  const mine = reconcileComposerDraft(
    reconcileInput({ ...picked, composerValue: "picked up, mine", cached: null }),
  );
  assert(mine === null, "this tab's typing stays");
});

run("reconcile keeps the sending tab's own pending text on its echo", () => {
  // The sender's composer was seeded (restored after a reload) and sent
  // unedited; its own acceptance/promotion echo must not blank it while the
  // POST can still fail.
  const r = reconcileComposerDraft(
    reconcileInput({
      conversationId: "sent1",
      isDraft: false,
      cached: { value: "leaving", basedOn: "", pending: true },
      ownsPending: true,
      composerValue: "leaving",
      lastSeededSession: "sent1",
      lastSeededValue: "leaving",
    }),
  );
  assert(r === null, "own pending entry is the composer's text");
});

run("reconcile restores a pending entry on first entry into the session", () => {
  // Reload while the send was still in flight: the text must not be lost.
  const r = reconcileComposerDraft(
    reconcileInput({
      conversationId: "sent1",
      isDraft: false,
      cached: { value: "leaving", basedOn: "", pending: true },
      lastSeededSession: undefined,
    }),
  );
  assert(r !== null && r.value === "leaving", "pending entry seeds on entry");
});

console.log("draftCache: all tests passed");
