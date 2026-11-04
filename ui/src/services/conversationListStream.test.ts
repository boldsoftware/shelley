import {
  applyConversationListPatch,
  reduceConversationListPatch,
  type ConversationListState,
} from "./conversationListStream";
import type {
  ConversationListPatchEvent,
  ConversationListPatchOp,
  ConversationWithState,
} from "../types";

function conv(id: string, slug: string, working = false): ConversationWithState {
  return {
    conversation_id: id,
    slug,
    user_initiated: true,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    cwd: null,
    archived: false,
    parent_conversation_id: null,
    model: null,
    conversation_options: "{}",
    current_generation: 0,
    agent_working: working,
    turn_interrupted: false,
    external_conversation_id: null,
    external_endpoint: null,
    next_prompt_suggestion: "",
    next_prompt_suggestion_seq: 0,
    tags: "[]",
    is_draft: false,
    draft: "",
    queued_messages: "[]",
    working,
    subagent_count: 0,
    running_background_jobs: 0,
    max_sequence_id: 0,
  };
}

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(`Assertion failed: ${message}`);
}

function run(name: string, fn: () => void): void {
  try {
    fn();
    console.log(`✓ ${name}`);
  } catch (err) {
    console.error(`✗ ${name}`);
    throw err;
  }
}

run("replace root", () => {
  const next = applyConversationListPatch(
    [],
    [{ op: "replace", path: "", value: [conv("a", "alpha")] }],
  );
  assert(next.length === 1, "expected one conversation");
  assert(next[0].slug === "alpha", "expected replaced root value");
});

run("add, replace field, remove", () => {
  let state = [conv("a", "alpha")];
  state = applyConversationListPatch(state, [{ op: "add", path: "/0", value: conv("b", "beta") }]);
  assert(state.map((c) => c.conversation_id).join(",") === "b,a", "expected inserted item");

  state = applyConversationListPatch(state, [{ op: "replace", path: "/1/working", value: true }]);
  assert(state[1].working, "expected field replacement");

  state = applyConversationListPatch(state, [{ op: "remove", path: "/0" }]);
  assert(state.length === 1 && state[0].conversation_id === "a", "expected removal");
});

run("move preserves row identity", () => {
  const original = [conv("a", "alpha"), conv("b", "beta")];
  const state = applyConversationListPatch(original, [{ op: "move", from: "/1", path: "/0" }]);
  assert(state.map((c) => c.conversation_id).join(",") === "b,a", "expected moved item");
  assert(state[0] === original[1], "expected moved row to retain its identity");
  assert(state[1] === original[0], "expected shifted row to retain its identity");
  assert(original[0].conversation_id === "a", "expected input order to remain unchanged");
});

run("field edits copy only the changed row", () => {
  const original = [conv("a", "alpha"), conv("b", "beta"), conv("c", "gamma")];
  const snapshot = JSON.stringify(original);
  const next = applyConversationListPatch(original, [
    { op: "replace", path: "/1/working", value: true },
    { op: "replace", path: "/1/slug", value: "updated" },
  ]);
  assert(next !== original, "expected a new list");
  assert(next[0] === original[0] && next[2] === original[2], "expected untouched row identities");
  assert(next[1] !== original[1], "expected changed row to be copied");
  assert(next[1].working && next[1].slug === "updated", "expected both edits to survive");
  assert(JSON.stringify(original) === snapshot, "expected input rows to remain unchanged");
});

run("editing a moved row copies its nested path without changing shared siblings", () => {
  const participants = [
    { email: "a@example.com", message_count: 1 },
    { email: "b@example.com", message_count: 1 },
  ];
  const original = [conv("a", "alpha"), { ...conv("b", "beta"), participants }];
  const snapshot = JSON.stringify(original);
  const next = applyConversationListPatch(original, [
    { op: "move", from: "/1", path: "/0" },
    { op: "replace", path: "/0/participants/0/message_count", value: 2 },
    { op: "replace", path: "/0/participants/0/email", value: "updated@example.com" },
  ]);
  assert(next[0] !== original[1], "expected the edited, moved row to be copied");
  assert(next[1] === original[0], "expected the unedited row to retain its identity");
  const nextParticipants = next[0].participants!;
  assert(nextParticipants !== participants, "expected participants array to be copied");
  assert(nextParticipants[0] !== participants[0], "expected edited participant to be copied");
  assert(nextParticipants[1] === participants[1], "expected untouched participant identity");
  assert(nextParticipants[0].message_count === 2, "expected nested count edit");
  assert(nextParticipants[0].email === "updated@example.com", "expected nested email edit");
  assert(JSON.stringify(original) === snapshot, "expected input tree to remain unchanged");
});

run("json pointer escaping", () => {
  const state = applyConversationListPatch(
    [{ ...conv("a", "alpha"), git_subject: "old" }],
    [{ op: "replace", path: "/0/git_subject", value: "slash / tilde ~ ok" }],
  );
  assert(state[0].git_subject === "slash / tilde ~ ok", "expected escaped path support");
});

run("throws on add past array end", () => {
  let threw = false;
  try {
    applyConversationListPatch([], [{ op: "add", path: "/5", value: conv("a", "a") }]);
  } catch (err) {
    threw = true;
    assert(err instanceof Error && /bad array index/.test(err.message), `unexpected error: ${err}`);
    assert(
      err instanceof Error && /len=/.test(err.message),
      "expected error message to include array length context",
    );
  }
  assert(threw, "expected applyConversationListPatch to throw");
});

run("throws on replace at missing index", () => {
  let threw = false;
  try {
    applyConversationListPatch(
      [conv("a", "a")],
      [{ op: "replace", path: "/5", value: conv("b", "b") }],
    );
  } catch (err) {
    threw = true;
    assert(
      err instanceof Error && /(out of range|bad array index)/.test(err.message),
      `unexpected error: ${err}`,
    );
  }
  assert(threw, "expected applyConversationListPatch to throw");
});

run("throws on remove at missing index", () => {
  let threw = false;
  try {
    applyConversationListPatch([], [{ op: "remove", path: "/0" }]);
  } catch (err) {
    threw = true;
    assert(err instanceof Error && /bad array index/.test(err.message), `unexpected error: ${err}`);
  }
  assert(threw, "expected applyConversationListPatch to throw");
});

run("does not mutate the input list", () => {
  const original = [conv("a", "alpha"), conv("b", "beta")];
  const snapshot = JSON.stringify(original);
  const next = applyConversationListPatch(original, [
    { op: "replace", path: "/0/working", value: true },
    { op: "remove", path: "/1" },
  ]);
  assert(JSON.stringify(original) === snapshot, "expected input list to be untouched");
  assert(next.length === 1 && next[0].working === true, "expected new list to reflect patch");
});

run("add, move, replace, remove stress sequence", () => {
  // Seeded xorshift32 keeps all arithmetic in 32 bits.
  let s = 0x12345;
  const rand = () => {
    s ^= s << 13;
    s ^= s >>> 17;
    s ^= s << 5;
    return s >>> 0;
  };
  let state: ConversationWithState[] = [];
  const expected: ConversationWithState[] = [];
  const counts = { add: 0, remove: 0, replace: 0, move: 0 };
  let nextId = 0;
  const id = () => `id-${nextId++}`;
  for (let batch = 0; batch < 100; batch++) {
    const original = state;
    const snapshot = JSON.stringify(original);
    const patch: ConversationListPatchOp[] = [];
    for (let step = 0; step < 5; step++) {
      const op = rand() % 4;
      if (op === 0 || expected.length === 0) {
        const at = rand() % (expected.length + 1);
        const value = conv(id(), "x");
        patch.push({ op: "add", path: `/${at}`, value });
        expected.splice(at, 0, { ...value });
        counts.add++;
      } else if (op === 1) {
        const at = rand() % expected.length;
        patch.push({ op: "remove", path: `/${at}` });
        expected.splice(at, 1);
        counts.remove++;
      } else if (op === 2) {
        const at = rand() % expected.length;
        const working = !expected[at].working;
        patch.push({ op: "replace", path: `/${at}/working`, value: working });
        expected[at] = { ...expected[at], working };
        counts.replace++;
      } else {
        if (expected.length < 2) continue;
        const from = rand() % expected.length;
        const to = (from + 1 + (rand() % (expected.length - 1))) % expected.length;
        patch.push({ op: "move", from: `/${from}`, path: `/${to}` });
        const [moved] = expected.splice(from, 1);
        expected.splice(to, 0, moved);
        counts.move++;
      }
    }
    state = applyConversationListPatch(original, patch);
    assert(
      JSON.stringify(state) === JSON.stringify(expected),
      `expected correct contents after batch ${batch}`,
    );
    assert(JSON.stringify(original) === snapshot, `batch ${batch} mutated its input`);
  }
  for (const [op, count] of Object.entries(counts)) {
    assert(count > 0, `expected stress sequence to exercise ${op}`);
  }
});

run("reduce: applies a chain of patches and advances hash atomically", () => {
  let state: ConversationListState = {
    list: [conv("a", "alpha"), conv("b", "beta")],
    hash: "h0",
  };
  const ev: ConversationListPatchEvent = {
    old_hash: "h0",
    new_hash: "h1",
    at: "",
    patch: [{ op: "replace", path: "/0/slug", value: "alpha2" }],
  };
  const res = reduceConversationListPatch(state, ev);
  assert(res.ok, "expected ok");
  if (res.ok) {
    assert(res.state.hash === "h1", "expected hash advanced to h1");
    assert(res.state.list[0].slug === "alpha2", "expected field replaced");
    state = res.state;
  }
});

run("reduce: rejects a patch whose old_hash doesn't anchor to current hash", () => {
  // This is the core regression: a patch BUILT against a newer state (h1->h2)
  // must NOT be applied to a list still at h0. Pre-fix, the hash ref could be
  // advanced to h1 while the list lagged at h0, sneaking this patch through
  // and corrupting the wrong row (wrong preview on the top conversation).
  const state: ConversationListState = {
    list: [conv("a", "alpha"), conv("b", "beta")],
    hash: "h0",
  };
  const ev: ConversationListPatchEvent = {
    old_hash: "h1",
    new_hash: "h2",
    at: "",
    patch: [{ op: "replace", path: "/0/preview", value: "WRONG" }],
  };
  const res = reduceConversationListPatch(state, ev);
  assert(!res.ok, "expected rejection");
  if (!res.ok) {
    assert(res.reason === "hash-mismatch", "expected hash-mismatch reason");
  }
  // State is untouched: the caller can recover via reconnect with both halves
  // still coherent.
  assert(state.hash === "h0", "expected hash unchanged");
  assert(state.list[0].slug === "alpha", "expected list unchanged");
});

run("reduce: reset bypasses the hash check and replaces wholesale", () => {
  const state: ConversationListState = { list: [conv("a", "alpha")], hash: "stale" };
  const ev: ConversationListPatchEvent = {
    old_hash: "unrelated",
    new_hash: "fresh",
    at: "",
    reset: true,
    patch: [{ op: "replace", path: "", value: [conv("b", "beta"), conv("c", "gamma")] }],
  };
  const res = reduceConversationListPatch(state, ev);
  assert(res.ok, "expected reset to apply");
  if (res.ok) {
    assert(res.state.hash === "fresh", "expected fresh hash");
    assert(res.state.list.map((c) => c.conversation_id).join(",") === "b,c", "expected new list");
    assert(res.removedIds.join(",") === "a", "expected 'a' reported removed");
  }
});

run("reduce: null current hash anchors to event with null old_hash", () => {
  const state: ConversationListState = { list: [], hash: null };
  const ev: ConversationListPatchEvent = {
    old_hash: null,
    new_hash: "h1",
    at: "",
    patch: [{ op: "add", path: "/0", value: conv("a", "alpha") }],
  };
  const res = reduceConversationListPatch(state, ev);
  assert(res.ok, "expected ok when both hashes are null");
  if (res.ok) assert(res.state.list.length === 1, "expected one conversation");
});

run("reduce: reports apply-failed without mutating state", () => {
  const state: ConversationListState = { list: [conv("a", "alpha")], hash: "h0" };
  const ev: ConversationListPatchEvent = {
    old_hash: "h0",
    new_hash: "h1",
    at: "",
    // Out-of-range index: the applier throws.
    patch: [{ op: "remove", path: "/5" }],
  };
  const res = reduceConversationListPatch(state, ev);
  assert(!res.ok, "expected failure");
  if (!res.ok) assert(res.reason === "apply-failed", "expected apply-failed reason");
  assert(state.hash === "h0" && state.list.length === 1, "expected state untouched");
});

run("reduce: failure after edits and a move leaves the prior state intact", () => {
  const list = [
    {
      ...conv("a", "alpha"),
      participants: [{ email: "a@example.com", message_count: 1 }],
    },
    conv("b", "beta"),
  ];
  const state: ConversationListState = { list, hash: "h0" };
  const snapshot = JSON.stringify(state);
  const res = reduceConversationListPatch(state, {
    old_hash: "h0",
    new_hash: "h1",
    at: "",
    patch: [
      { op: "replace", path: "/0/working", value: true },
      { op: "replace", path: "/0/participants/0/message_count", value: 2 },
      { op: "move", from: "/0", path: "/1" },
      { op: "remove", path: "/5" },
    ],
  });
  assert(!res.ok, "expected failure");
  if (!res.ok) {
    assert(res.reason === "apply-failed", "expected apply-failed reason");
    if (res.reason === "apply-failed") {
      assert(
        res.error instanceof Error &&
          res.error.message.includes("bad array index in patch path: /5"),
        "expected failure at the final operation, after the valid edits",
      );
    }
  }
  assert(state.list === list, "expected the original list reference");
  assert(JSON.stringify(state) === snapshot, "expected all original contents and hash unchanged");
});

console.log("\nConversationListStream tests passed");
