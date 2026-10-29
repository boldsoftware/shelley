import assert from "node:assert/strict";
import { test, type TestContext } from "node:test";
import { customizationsApi, type Customization } from "../../services/customizations";
import { createCustomizationsState } from "./customizations";

const installed = (): Customization => ({
  conversation_id: "task",
  commit: "123456789",
  artifact_sha256: "artifact",
  finished: false,
  status: "installed",
  running_under_systemd: true,
  error: "",
  updated_at: "",
});
const settle = async () => {
  for (let i = 0; i < 12; i++) await Promise.resolve();
};
function harness(t: TestContext) {
  t.mock.timers.enable({ apis: ["setTimeout", "Date"] });
  let visible = true;
  const state = createCustomizationsState(() => visible);
  t.after(() => state.dispose());
  return {
    state,
    hide: () => {
      visible = false;
      state.pause();
    },
  };
}

test("polling detects dirty checkout even when no action runs; hidden nav pauses", async (t) => {
  const { state, hide } = harness(t);
  let current = installed();
  const fetch = t.mock.method(customizationsApi, "list", async () => [current]);
  await state.refresh();
  assert.equal(state.entries.value[0].status, "installed");
  current = { ...current, status: "stale" };
  t.mock.timers.tick(5000);
  await settle();
  assert.equal(state.entries.value[0].status, "stale");
  hide();
  t.mock.timers.tick(5000);
  await settle();
  assert.equal(fetch.mock.callCount(), 2);
});

test("restart refusal remains visible after successful status refresh", async (t) => {
  const { state } = harness(t);
  t.mock.method(customizationsApi, "list", async () => [installed()]);
  t.mock.method(customizationsApi, "restart", async () => {
    throw new Error("Wait for running commands");
  });
  await state.refresh();
  await state.mutate(installed(), "restart");
  assert.equal(state.requestError.value, "Wait for running commands");
  assert.equal(state.error.value, "");
  assert.equal(state.pendingConversation.value, "");
});

test("an old GET cannot overwrite an acknowledged verification; duplicate mutations are ignored", async (t) => {
  const { state } = harness(t);
  let finishGet!: (value: Customization[]) => void;
  let finishVerify!: (value: Customization) => void;
  const fetch = t.mock.method(
    customizationsApi,
    "list",
    () =>
      new Promise<Customization[]>((resolve) => {
        finishGet = resolve;
      }),
  );
  const verify = t.mock.method(
    customizationsApi,
    "verify",
    () =>
      new Promise<Customization>((resolve) => {
        finishVerify = resolve;
      }),
  );
  const old = state.refresh();
  const mutation = state.mutate(installed(), "verify");
  await state.mutate(installed(), "verify");
  assert.equal(verify.mock.callCount(), 1);
  const verifying: Customization = { ...installed(), status: "verifying" };
  finishVerify(verifying);
  await settle();
  finishGet([installed()]);
  await old;
  await settle();
  assert.equal(state.entries.value[0].status, "verifying");
  assert.equal(fetch.mock.callCount(), 2);
  finishGet([verifying]);
  await mutation;
  assert.equal(state.entries.value[0].status, "verifying");
});

test("an ambiguous restart request reconciles server state but never resubmits installation", async (t) => {
  const { state } = harness(t);
  const restart = t.mock.method(customizationsApi, "restart", async () => {
    throw new Error("Connection closed");
  });
  t.mock.method(customizationsApi, "list", async () => [
    { ...installed(), status: "running" as const },
  ]);
  await state.mutate(installed(), "restart");
  assert.equal(state.entries.value[0].status, "running");
  assert.equal(restart.mock.callCount(), 1);
});

test("cancelling an auto-restart posts the build and refreshes", async (t) => {
  const { state } = harness(t);
  let current: Customization = {
    ...installed(),
    auto_restart: "pending",
    auto_restart_in_ms: 9000,
  };
  t.mock.method(customizationsApi, "list", async () => [current]);
  const cancel = t.mock.method(customizationsApi, "cancelAutoRestart", async () => {
    current = installed();
    return current;
  });
  await state.refresh();
  await state.mutate(current, "cancelAutoRestart");
  assert.equal(cancel.mock.callCount(), 1);
  assert.equal(state.entries.value[0].auto_restart, undefined);
});
