import assert from "node:assert/strict";
import { test, type TestContext } from "node:test";
import { effectScope, nextTick, ref } from "vue";
import type { api } from "../../services/api";
import { useSkillCompletion } from "./skillCompletion";

type Response = Awaited<ReturnType<typeof api.getSkills>>;
const response = (name: string): Response => ({
  skills: [{ name, description: `Help with ${name}`, activate: `load ${name}` }],
});
async function harness(t: TestContext) {
  const message = ref("/skills");
  const cwd = ref("/work");
  const session = ref<string | null>(null);
  const conversationId = ref<string | null>(null);
  const enabled = ref(true);
  const requests: {
    cwd: string;
    conversationId: string | null;
    signal?: AbortSignal;
    resolve: (r: Response) => void;
    reject: (e: Error) => void;
  }[] = [];
  const scope = effectScope();
  const completion = scope.run(() =>
    useSkillCompletion({
      message,
      cwd: () => cwd.value,
      session: () => session.value,
      conversationId: () => conversationId.value,
      enabled: () => enabled.value,
      getSkills: (cwd, conversationId, signal) =>
        new Promise<Response>((resolve, reject) =>
          requests.push({ cwd, conversationId, signal, resolve, reject }),
        ),
    }),
  )!;
  t.after(() => scope.stop());
  completion.updateSelection(7, 7);
  completion.focused.value = true;
  function input(text: string) {
    message.value = text;
    completion.updateSelection(text.length, text.length);
  }
  await nextTick();
  return { completion, input, message, cwd, session, conversationId, enabled, requests, scope };
}

test("loads one catalog per open, filters locally as the query grows and shrinks, and inserts", async (t) => {
  const h = await harness(t);
  assert.equal(h.completion.visible.value, true);
  assert.equal(h.completion.loading.value, true);
  assert.equal(h.completion.choose(0), null);
  assert.equal(h.requests[0].cwd, "/work");
  assert.equal(h.requests[0].conversationId, null);
  h.requests[0].resolve({ skills: [...response("foo").skills, ...response("bar").skills] });
  await Promise.resolve();
  h.completion.selected.value = 1;
  h.input("/skills HELP FOO");
  assert.equal(h.completion.selected.value, 0);
  assert.deepEqual(
    h.completion.matches.value.map((s) => s.name),
    ["foo"],
  );
  h.input("/skills");
  assert.equal(h.requests.length, 1);
  assert.equal(h.completion.matches.value.length, 2);
  const replacement = h.completion.choose(0)!;
  assert.match(replacement.text, /`load foo`/);
  assert.equal(h.completion.visible.value, false);
  h.input(replacement.text);
  assert.equal(h.completion.active.value, false);
});

test("stale successful and failed responses cannot overwrite a new cwd or conversation catalog", async (t) => {
  const h = await harness(t);
  h.cwd.value = "/other";
  assert.equal(h.requests[0].signal?.aborted, true);
  await nextTick();
  h.session.value = "conversation-two";
  h.conversationId.value = "conversation-two";
  assert.equal(h.requests[1].signal?.aborted, true);
  await nextTick();
  assert.equal(h.requests[2].cwd, "/other");
  assert.equal(h.requests[2].conversationId, "conversation-two");
  assert.equal(h.completion.choose(0), null);
  h.requests[2].resolve(response("current"));
  await Promise.resolve();
  h.requests[0].resolve(response("old"));
  h.requests[1].reject(new Error("stale failure"));
  await Promise.resolve();
  assert.equal(h.completion.error.value, "");
  assert.equal(h.completion.loading.value, false);
  assert.equal(h.completion.matches.value[0].name, "current");
});

test("Escape persists through caret movement and refocus, editing or explicit reopen works", async (t) => {
  const h = await harness(t);
  h.input("/skills foo");
  h.completion.dismiss();
  assert.equal(h.requests[0].signal?.aborted, true);
  h.completion.updateSelection(8, 8);
  h.completion.focused.value = false;
  h.completion.focused.value = true;
  assert.equal(h.completion.visible.value, false);
  assert.equal(h.completion.active.value, true);
  h.input("/skills bar");
  assert.equal(h.completion.visible.value, true);
  await nextTick();
  assert.equal(h.requests.length, 2);
  h.completion.dismiss();
  h.completion.reopen();
  await nextTick();
  assert.equal(h.requests.length, 3);
  h.completion.dismiss();
  h.input("");
  h.input("/skills bar");
  assert.equal(h.completion.visible.value, true);
});

test("loading, errors, and empty catalogs cannot select; reopen retries", async (t) => {
  const h = await harness(t);
  h.requests[0].reject(new Error("Permission denied"));
  await Promise.resolve();
  assert.equal(h.completion.error.value, "Permission denied");
  assert.equal(h.completion.loading.value, false);
  assert.equal(h.completion.choose(0), null);
  h.completion.dismiss();
  h.completion.reopen();
  assert.equal(h.completion.error.value, "");
  await nextTick();
  h.requests[1].resolve({ skills: [] });
  await Promise.resolve();
  assert.equal(h.completion.matches.value.length, 0);
  assert.equal(h.completion.choose(0), null);
});

test("blur, disabled/shell mode, and disposal abort pending work and reject late responses", async (t) => {
  const h = await harness(t);
  h.completion.focused.value = false;
  assert.equal(h.requests[0].signal?.aborted, true);
  h.completion.focused.value = true;
  await nextTick();
  h.enabled.value = false;
  assert.equal(h.completion.active.value, false);
  assert.equal(h.completion.visible.value, false);
  assert.equal(h.requests[1].signal?.aborted, true);
  h.enabled.value = true;
  await nextTick();
  h.scope.stop();
  assert.equal(h.requests[2].signal?.aborted, true);
  h.requests.forEach((r) => r.resolve(response("late")));
  await Promise.resolve();
  assert.equal(h.completion.matches.value.length, 0);
});

test("query updates during loading use the new filter without rediscovery", async (t) => {
  const h = await harness(t);
  h.input("/skills bar");
  h.requests[0].resolve({ skills: [...response("foo").skills, ...response("bar").skills] });
  await Promise.resolve();
  assert.deepEqual(
    h.completion.matches.value.map((s) => s.name),
    ["bar"],
  );
  assert.equal(h.requests.length, 1);
});

test("lazy draft identity scopes requests even while its stable composer session stays null", async (t) => {
  const h = await harness(t);
  h.conversationId.value = "lazy-draft";
  assert.equal(h.session.value, null);
  assert.equal(h.requests[0].signal?.aborted, true);
  assert.equal(h.completion.choose(0), null);
  h.requests[0].resolve(response("stale-discovery"));
  await nextTick();
  assert.equal(h.requests.length, 2);
  assert.equal(h.requests[1].conversationId, "lazy-draft");
  assert.equal(h.completion.matches.value.length, 0);
  h.requests[1].resolve(response("draft-discovery"));
  await Promise.resolve();
  assert.equal(h.completion.matches.value[0].name, "draft-discovery");

  // Sending promotes the row without changing either ID. The next open must
  // still send the actual ID so the server selects its persisted prompt.
  h.input("First message");
  h.input("/skills");
  await nextTick();
  assert.equal(h.session.value, null);
  assert.equal(h.requests[2].conversationId, "lazy-draft");
  h.requests[2].resolve(response("persisted-snapshot"));
  await Promise.resolve();
  assert.equal(h.completion.matches.value[0].name, "persisted-snapshot");
});

test("context changes invalidate immediately but batch transient prop combinations into one request", async (t) => {
  const h = await harness(t);
  h.requests[0].resolve(response("old"));
  await Promise.resolve();
  // Vue patches conversationId and lazyDraftId separately; the stable session
  // temporarily changes before its batched watcher sees the settled props.
  h.conversationId.value = "new-draft";
  h.session.value = "new-draft";
  assert.equal(h.completion.matches.value.length, 0);
  assert.equal(h.completion.loading.value, true);
  assert.equal(h.completion.choose(0), null);
  h.session.value = null;
  h.cwd.value = "/new-work";
  assert.equal(h.requests.length, 1);
  await nextTick();
  assert.equal(h.requests.length, 2);
  assert.equal(h.requests[1].conversationId, "new-draft");
  assert.equal(h.requests[1].cwd, "/new-work");
});

test("closing or disposing before the next tick cancels scheduled discovery", async (t) => {
  const h = await harness(t);
  h.conversationId.value = "next-conversation";
  h.completion.dismiss();
  await nextTick();
  assert.equal(h.requests.length, 1);
  assert.equal(h.requests[0].signal?.aborted, true);
  h.completion.reopen();
  h.scope.stop();
  await nextTick();
  assert.equal(h.requests.length, 1);
});
