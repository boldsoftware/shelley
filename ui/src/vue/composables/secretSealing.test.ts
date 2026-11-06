import assert from "node:assert/strict";
import { test } from "node:test";
import { ref } from "vue";
import { useSecretSealing } from "./secretSealing";

// Split, so the source holds no key-shaped literal for secret scanners.
const key = "ghp_" + "Ab3dEf6hIj9kLm2nOp5qRs8tUv1wXy4zA1b2";

function fakeFetch(fail = false) {
  const calls: Array<{ method: string; url: string; body: Record<string, string> }> = [];
  globalThis.fetch = (async (url: string, init: RequestInit) => {
    calls.push({
      method: init.method ?? "GET",
      url,
      body: init.body ? JSON.parse(init.body as string) : {},
    });
    return new Response(fail ? "boom" : null, { status: fail ? 500 : 204 });
  }) as typeof fetch;
  return calls;
}

test("seals once the caret leaves, keeps the caret, undoes", async () => {
  const calls = fakeFetch();
  const s = useSecretSealing(ref(true), () => "c1");
  const text = `token ${key}`;
  assert.deepEqual(s.seal(text, text.length, false), { text, caret: text.length });
  assert.ok(s.holdsUnsealed(text));
  const typed = `${text} ok`;
  const out = s.seal(typed, typed.length, false);
  const id = s.sealed.value[0].id;
  assert.match(id, /^github-pat-[a-z0-9]{4}$/);
  assert.equal(out.text, `token {{secret:${id}}} ok`);
  assert.equal(out.caret, out.text.length);
  assert.ok(!s.holdsUnsealed(out.text));
  await s.ready();
  assert.deepEqual(calls[0].body, {
    id,
    name: "GitHub PAT",
    kind: "github-pat",
    value: key,
    conversation_id: "c1",
  });

  const back = s.undo(id, out.text);
  assert.equal(back.text, typed);
  assert.equal(s.seal(back.text, 0, true).text, back.text, "undone values stay");
  await back.deleted;
  assert.equal(calls[1].method, "DELETE");
  assert.equal(calls[1].url, `/api/secrets/${id}`);
});

test("marks seal when closed; failed uploads block the send", async () => {
  fakeFetch(true);
  const s = useSecretSealing(ref(true), () => undefined);
  const out = s.seal("pw {{secret:db=hunter2}}", 24, false);
  assert.match(out.text, /^pw \{\{secret:db-[a-z0-9]{4}\}\}$/);
  await assert.rejects(s.ready(), /Could not store secret db-/);
});

test("off: nothing happens", () => {
  const s = useSecretSealing(ref(false), () => undefined);
  assert.equal(s.seal(key, 0, true).text, key);
  assert.ok(!s.holdsUnsealed(key));
});
