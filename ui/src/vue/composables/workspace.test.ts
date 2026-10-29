import assert from "node:assert/strict";
import { test, type TestContext } from "node:test";
import { workspaceApi, type ActionRunSummary, type Workspace } from "../../services/workspace";
import { en } from "../../i18n/en";
import type { TranslationKeys } from "../../i18n/types";
import { createWorkspaceState, projectDeletionPlan } from "./workspace";

const translate = (key: keyof TranslationKeys) => en[key];

const empty = (): Workspace => ({
  assistant_id: "assistant",
  projects: [
    {
      name: "Blog",
      instructions: "Writing\n\nBe brief",
      cwd: "/tmp",
      actions: [],
    },
  ],
  conversations: [
    { conversation_id: "task", project: "Blog", is_assistant: false, archived: false },
  ],
  runs: [],
});
const run = (): ActionRunSummary => ({
  id: "run",
  conversation_id: "task",
  project: "Blog",
  action_id: "publish",
  label: "Publish",
  status: "running",
  started_at: "2026-01-01T12:00:00Z",
  report: "pending",
});
const settle = async () => {
  for (let i = 0; i < 8; i++) await Promise.resolve();
};
function harness(t: TestContext) {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const finished: ActionRunSummary[] = [];
  const workspace = createWorkspaceState(translate, (run) => finished.push(run));
  t.after(() => workspace.dispose());
  return { workspace, finished };
}

test("idle snapshots are SSE-driven and identical snapshots preserve references", async (t) => {
  const { workspace } = harness(t);
  const fetch = t.mock.method(workspaceApi, "workspace", async () => empty());
  await workspace.refresh();
  const first = workspace.data.value;
  assert.equal(workspace.isAssistant("assistant"), true);
  assert.equal(workspace.isAssistant(null), false);
  assert.equal(workspace.projectFor("task")?.name, "Blog");
  assert.equal(workspace.projectFor("assistant"), undefined);
  t.mock.timers.tick(10_000);
  await settle();
  assert.equal(fetch.mock.callCount(), 1, "no idle polling");
  await workspace.refresh();
  assert.equal(workspace.data.value, first, "unchanged workspace does not rerender projects");
});

test("refreshes report a run's completion once, and running runs don't poll", async (t) => {
  const { workspace, finished } = harness(t);
  let snapshot = empty();
  const fetch = t.mock.method(workspaceApi, "workspace", async () => structuredClone(snapshot));
  await workspace.refresh();
  const active = run();
  workspace.rememberRun(active);
  t.mock.timers.tick(10_000);
  await settle();
  assert.equal(fetch.mock.callCount(), 1, "no output polling");
  snapshot = { ...empty(), runs: [{ ...active, status: "succeeded" }] };
  await workspace.refresh();
  assert.equal(finished.length, 1);
  assert.equal(finished[0].status, "succeeded");
  await workspace.refresh();
  assert.equal(finished.length, 1);
});

test("failed refresh propagates errors, retains the last snapshot, and retry clears the error", async (t) => {
  const { workspace } = harness(t);
  let fail = false;
  t.mock.method(workspaceApi, "workspace", async () => {
    if (fail) throw new Error("offline");
    return empty();
  });
  await workspace.refresh();
  const previous = workspace.data.value;
  fail = true;
  await assert.rejects(workspace.refresh(), /offline/);
  assert.equal(workspace.error.value, "offline");
  assert.equal(workspace.data.value, previous);
  fail = false;
  await workspace.refresh();
  assert.equal(workspace.error.value, null);
});

test("an in-flight snapshot cannot erase a newly started action", async (t) => {
  const { workspace } = harness(t);
  t.mock.method(workspaceApi, "workspace", async () => empty());
  await workspace.refresh();
  let resolve!: (value: Workspace) => void;
  let calls = 0;
  t.mock.method(workspaceApi, "workspace", () => {
    calls++;
    if (calls === 1)
      return new Promise<Workspace>((done) => {
        resolve = done;
      });
    return Promise.resolve({ ...empty(), runs: [run()] });
  });
  const pending = workspace.refresh();
  workspace.rememberRun(run());
  resolve(empty());
  await pending;
  assert.equal(calls, 2);
  assert.equal(workspace.data.value?.runs[0].id, "run");
});

test("mutations during a refresh coalesce into one fresh follow-up snapshot", async (t) => {
  const { workspace } = harness(t);
  let resolve!: (value: Workspace) => void;
  let calls = 0;
  t.mock.method(workspaceApi, "workspace", () => {
    calls++;
    if (calls === 1)
      return new Promise<Workspace>((done) => {
        resolve = done;
      });
    return Promise.resolve({
      ...empty(),
      projects: [{ ...empty().projects[0], name: "Renamed" }],
      conversations: [
        { conversation_id: "task", project: "Renamed", is_assistant: false, archived: false },
      ],
    });
  });
  const pending = workspace.refresh();
  const again = workspace.refresh();
  workspace.refresh();
  assert.equal(pending, again);
  resolve(empty());
  await pending;
  assert.equal(calls, 2);
  assert.equal(workspace.projectFor("task")?.name, "Renamed");
});

test("project deletion plan: active conversations and running actions block; archived ones move", () => {
  const data = empty();
  const blog = data.projects[0];
  assert.equal(
    projectDeletionPlan(data, blog, translate).blocker,
    "Archive or move its active conversation first",
  );
  assert.equal(projectDeletionPlan(null, blog, translate).blocker, en.loading);

  data.conversations[0].archived = true;
  data.runs = [run()];
  assert.equal(
    projectDeletionPlan(data, blog, translate).blocker,
    "Wait for its running action to finish",
  );

  data.runs = [];
  assert.deepEqual(projectDeletionPlan(data, blog, translate), { blocker: "", archived: 1 });
});
