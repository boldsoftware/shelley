import assert from "node:assert/strict";
import { COMPOSER_ROW_ID, withComposerRow, type ComposerRow } from "./composerRow";
import type { ConversationWithState } from "../types";

function conv(id: string, extra: Partial<ConversationWithState> = {}): ConversationWithState {
  return {
    conversation_id: id,
    slug: id,
    user_initiated: true,
    created_at: "2026-05-10T12:00:00Z",
    updated_at: "2026-05-10T12:00:00Z",
    cwd: "/repo",
    archived: false,
    parent_conversation_id: null,
    model: null,
    conversation_options: "{}",
    current_generation: 0,
    agent_working: false,
    turn_interrupted: false,
    external_conversation_id: null,
    external_endpoint: null,
    next_prompt_suggestion: "",
    next_prompt_suggestion_seq: 0,
    tags: "[]",
    is_draft: false,
    draft: "",
    queued_messages: "[]",
    working: false,
    subagent_count: 0,
    running_background_jobs: 0,
    max_sequence_id: 0,
    ...extra,
  };
}

function row(extra: Partial<ComposerRow> = {}): ComposerRow {
  return {
    id: null,
    cwd: "/repo",
    startedAt: "2026-05-10T12:05:00Z",
    sent: null,
    listed: false,
    ...extra,
  };
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

const older = conv("c1", { git_commit: "abc", git_subject: "fix things" });

run("no composer leaves the list alone", () => {
  const list = [older];
  assert.equal(withComposerRow(list, null, null), list);
});

run("an unsaved composer is a draft placeholder at the top", () => {
  const [top, ...rest] = withComposerRow([older], row(), null);
  assert.equal(top.conversation_id, COMPOSER_ROW_ID);
  assert.equal(top.is_draft, true);
  assert.equal(top.working, false);
  assert.equal(top.updated_at, "2026-05-10T12:05:00Z");
  // Borrowed from the latest conversation in the same cwd.
  assert.equal(top.git_subject, "fix things");
  assert.deepEqual(rest, [older]);
});

run("a saved draft the list has yet to report keeps its placeholder, under its id", () => {
  const [top] = withComposerRow([older], row({ id: "c2" }), null);
  assert.equal(top.conversation_id, "c2");
  assert.equal(top.is_draft, true);
});

run("the server's draft row replaces the placeholder", () => {
  const draft = conv("c2", { is_draft: true, draft: "hi" });
  const list = [draft, older];
  assert.deepEqual(withComposerRow(list, row({ id: "c2" }), null), list);
});

run("a sent placeholder previews the message and works", () => {
  const [top] = withComposerRow([older], row({ sent: "hello" }), null);
  assert.equal(top.conversation_id, COMPOSER_ROW_ID);
  assert.equal(top.is_draft, false);
  assert.equal(top.working, true);
  assert.equal(top.preview, "hello");
});

run("a sent draft shows as sent until the server promotes it and the agent replies", () => {
  const sent = row({ id: "c2", sent: "hello" });
  let [top] = withComposerRow([conv("c2", { is_draft: true, draft: "hello" })], sent, null);
  assert.equal(top.is_draft, false);
  assert.equal(top.draft, "");
  assert.equal(top.working, true);
  assert.equal(top.preview, "hello");

  [top] = withComposerRow([conv("c2", { slug: null, working: true })], sent, null);
  assert.equal(top.preview, "hello");

  const replied = conv("c2", { working: true, preview: "Hi! What next?" });
  assert.equal(withComposerRow([replied], sent, null)[0], replied);
});

run("while a create is pending, new rows are hidden behind the placeholder", () => {
  const creating = new Set(["c1"]);
  const draft = conv("c4", { is_draft: true });
  const subagent = conv("c5", { parent_conversation_id: "c1" });
  const list = [draft, subagent, older];
  const ids = (l: ConversationWithState[], r: ComposerRow, c: ReadonlySet<string> | null) =>
    withComposerRow(l, r, c).map((x) => x.conversation_id);
  assert.deepEqual(ids(list, row(), creating), [COMPOSER_ROW_ID, "c5", "c1"]);
  // No create pending: nothing new can be the composer's.
  assert.deepEqual(ids(list, row(), null), [COMPOSER_ROW_ID, "c4", "c5", "c1"]);

  const created = conv("c3", { slug: null, working: true });
  const waiting = row({ sent: "hello" });
  assert.deepEqual(ids([created, older], waiting, creating), [COMPOSER_ROW_ID, "c1"]);

  // With the id known, that row is the composer's.
  const answered = withComposerRow([created, older], { ...waiting, id: "c3" }, creating);
  assert.deepEqual(
    answered.map((c) => [c.conversation_id, c.preview]),
    [
      ["c3", "hello"],
      ["c1", undefined],
    ],
  );
});

run("once listed, a row removed from the list stays gone", () => {
  assert.deepEqual(withComposerRow([older], row({ id: "c2", listed: true }), null), [older]);
  const sent = row({ id: "c2", sent: "hello", listed: true });
  assert.deepEqual(withComposerRow([older], sent, null), [older]);
});
