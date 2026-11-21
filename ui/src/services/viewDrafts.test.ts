import assert from "node:assert/strict";
import { test } from "node:test";
import { offerViewDraft, openComposer, viewDraftFor, withdrawViewDrafts } from "./viewDrafts";

const idle = () => false;

test("a draft needs the conversation's text box showing", async () => {
  await assert.rejects(offerViewDraft("hidden", {}, "A", "hi"), /isn't showing/);
  const close = openComposer("c", idle);
  const second = openComposer("c", () => false);
  close();
  const sent = offerViewDraft("c", {}, "A", "hi");
  assert.equal(viewDraftFor("c")?.text, "hi");
  viewDraftFor("c")?.settle(true);
  assert.equal(await sent, true);
  assert.equal(viewDraftFor("c"), undefined);
  second();
  await assert.rejects(offerViewDraft("c", {}, "A", "hi"), /isn't showing/);
});

test("a newer draft takes the place of the last one", async () => {
  const close = openComposer("c", idle);
  const first = offerViewDraft("c", {}, "A", "one");
  const last = offerViewDraft("c", {}, "B", "two");
  assert.equal(await first, false);
  assert.equal(viewDraftFor("c")?.text, "two");
  viewDraftFor("c")?.settle(true);
  viewDraftFor("c")?.settle(false);
  assert.equal(await last, true);
  close();
});

test("a busy text box turns views away", async () => {
  let busy = false;
  const close = openComposer("c", () => busy);
  busy = true;
  await assert.rejects(offerViewDraft("c", {}, "A", "hi"), /busy/);
  busy = false;
  const going = offerViewDraft("c", {}, "A", "going");
  viewDraftFor("c")!.sending = true;
  await assert.rejects(offerViewDraft("c", {}, "B", "next"), /busy/);
  assert.equal(viewDraftFor("c")?.text, "going");
  viewDraftFor("c")?.settle(true);
  assert.equal(await going, true);
  close();
});

test("a view that goes takes back only its own drafts", async () => {
  const closeC = openComposer("c", idle);
  const closeD = openComposer("d", idle);
  const view = {};
  const mine = offerViewDraft("c", view, "A", "mine");
  const theirs = offerViewDraft("d", {}, "B", "theirs");
  withdrawViewDrafts(view);
  assert.equal(await mine, false);
  assert.equal(viewDraftFor("c"), undefined);
  assert.equal(viewDraftFor("d")?.text, "theirs");
  viewDraftFor("d")?.settle(true);
  assert.equal(await theirs, true);
  closeC();
  closeD();
});
