import assert from "node:assert/strict";
import { test } from "node:test";
import { customizationsApi, type Customization } from "../../services/customizations";
import { createCustomizationsState } from "./customizations";

const installed = (): Customization => ({
  conversation_id: "task",
  commit: "123456789",
  status: "installed",
  running_under_systemd: true,
  error: "",
  updated_at: "",
});

test("the stream's states replace the entries, stamped with their restart deadline", (t) => {
  t.mock.timers.enable({ apis: ["Date"], now: 1000 });
  const state = createCustomizationsState();
  state.apply([{ ...installed(), auto_restart: "pending", auto_restart_in_ms: 9000 }]);
  assert.equal(state.entries.value[0].auto_restart_at, 10_000);
  state.apply([]);
  assert.deepEqual(state.entries.value, []);
});

test("a refused restart stays visible; the entry is kept", async (t) => {
  const state = createCustomizationsState();
  state.apply([installed()]);
  t.mock.method(customizationsApi, "restart", async () => {
    throw new Error("Wait for running commands");
  });
  await state.mutate(installed(), "restart", "drawer");
  assert.deepEqual(state.requestError.value, {
    message: "Wait for running commands",
    conversationId: "task",
    place: "drawer",
  });
  assert.equal(state.entries.value[0].status, "installed");
  assert.equal(state.pendingConversation.value, "");
});

test("a restart applies its answer; a second click meanwhile is ignored", async (t) => {
  const state = createCustomizationsState();
  state.apply([installed()]);
  let finish!: (value: Customization) => void;
  const restart = t.mock.method(
    customizationsApi,
    "restart",
    () =>
      new Promise<Customization>((resolve) => {
        finish = resolve;
      }),
  );
  const first = state.mutate(installed(), "restart", "drawer");
  await state.mutate(installed(), "restart", "drawer");
  assert.equal(restart.mock.callCount(), 1);
  finish({ ...installed(), status: "restarting" });
  await first;
  assert.equal(state.entries.value.length, 1);
  assert.equal(state.entries.value[0].status, "restarting");
});

test("cancelling an auto-restart applies the answer", async (t) => {
  const state = createCustomizationsState();
  state.apply([{ ...installed(), auto_restart: "pending", auto_restart_in_ms: 9000 }]);
  const cancel = t.mock.method(customizationsApi, "cancelAutoRestart", async () => installed());
  await state.mutate(state.entries.value[0], "cancelAutoRestart", "header");
  assert.equal(cancel.mock.callCount(), 1);
  assert.equal(state.entries.value[0].auto_restart, undefined);
});
