import assert from "node:assert/strict";
import { test, type TestContext } from "node:test";
import { workspaceApi, type ActionRunSummary, type Workspace } from "../../services/workspace";
import { en } from "../../i18n/en";
import type { TranslationKeys } from "../../i18n/types";
import {
  createWorkspaceState,
  projectDeletionPlan,
  projectNameProblem,
  projectViewFromPath,
  projectViewPath,
} from "./workspace";

const translate = (key: keyof TranslationKeys) => en[key];

const empty = (): Workspace => ({
  assistant_id: "assistant",
  projects: [
    {
      id: 1,
      position: 0,
      name: "Blog",
      instructions: "Writing\n\nBe brief",
      cwd: "/tmp",
      actions: [],
      vm: null,
      repos: [],
      page_url: "",
    },
  ],
  conversations: [
    { conversation_id: "task", project: "Blog", is_assistant: false, archived: false },
  ],
  runs: [],
  vms: [],
  base_vms: [],
  shelley_project: "",
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

test("archiving a conversation on its own VM warns that the VM is deleted", (t) => {
  const { workspace } = harness(t);
  const vm = {
    conversation_id: "task",
    vm_name: "base-task",
    base_vm: "base",
    integration: "exe",
    shared: false,
    status: "ready" as const,
    error: "",
    ssh_dest: "",
    https_url: "",
    dir: "/home/exedev/blog",
    conversation_archived: false,
    replaced: false,
  };
  workspace.data.value = { ...empty(), vms: [vm] };
  assert.match(workspace.archiveNote("task"), /deletes VM base-task; uncommitted work/);
  assert.equal(workspace.archiveNote("other"), "");
  // One it moved away from is deleted too, but is not where it runs.
  workspace.data.value = {
    ...empty(),
    vms: [{ ...vm, vm_name: "shelley-task", replaced: true }, vm],
  };
  assert.match(workspace.archiveNote("task"), /deletes VMs shelley-task, base-task; uncommitted/);
  assert.equal(workspace.vmFor("task")?.vm_name, "base-task");
  workspace.data.value = { ...empty(), vms: [{ ...vm, status: "deleted" }] };
  assert.equal(workspace.archiveNote("task"), "");
  // A shared VM outlives its conversations.
  workspace.data.value = {
    ...empty(),
    vms: [{ ...vm, vm_name: "hub", base_vm: "", shared: true }],
  };
  assert.equal(workspace.archiveNote("task"), "");
});

test("every archive route confirms deleting the conversation's own VM first", async (t) => {
  const { workspace } = harness(t);
  const vm = {
    conversation_id: "task",
    vm_name: "base-task",
    base_vm: "base",
    integration: "exe",
    shared: false,
    status: "ready" as const,
    error: "",
    ssh_dest: "",
    https_url: "",
    dir: "/home/exedev/blog",
    conversation_archived: false,
    replaced: false,
  };
  workspace.data.value = { ...empty(), vms: [vm] };
  const archived: string[] = [];
  const archive = (id: string) => () => {
    archived.push(id);
  };
  // Without a VM to delete, archiving needs no confirmation.
  await workspace.confirmArchive("other", archive("other"));
  assert.deepEqual(archived, ["other"]);
  const pending = () => workspace.archiveConfirmation.value;
  assert.equal(pending(), null);

  const cancelled = workspace.confirmArchive("task", archive("task"));
  assert.match(pending()?.note ?? "", /deletes VM base-task/);
  workspace.answerArchive(false);
  await cancelled;
  assert.deepEqual(archived, ["other"]);
  assert.equal(pending(), null);

  // A second request replaces (and cancels) an unanswered one.
  const replaced = workspace.confirmArchive("task", archive("first"));
  const confirmed = workspace.confirmArchive("task", archive("task"));
  await replaced;
  workspace.answerArchive(true);
  await confirmed;
  assert.deepEqual(archived, ["other", "task"]);
});

test("project deletion plan: active conversations block; the setup conversation stops; only Shelley's VMs go", () => {
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
  const setup = {
    ...blog,
    vm: {
      managed_base_vm: true,
      base: "",
      shared_vm: "",
      integration: "i",
      setup_conversation_id: "setup",
    },
  };
  data.conversations.push({
    conversation_id: "setup",
    project: "Blog",
    is_assistant: false,
    archived: false,
  });
  data.base_vms = [{ project: "Blog", vm_name: "blog-base", integration: "i", status: "creating" }];
  assert.deepEqual(projectDeletionPlan(data, setup, translate), {
    blocker: "",
    setupId: "setup",
    archived: 1,
    deletesVMs: ["blog-base"],
    keepsVMs: [],
  });

  data.base_vms = [];
  const own = {
    ...blog,
    vm: {
      managed_base_vm: false,
      base: "mine",
      shared_vm: "",
      integration: "i",
      setup_conversation_id: "",
    },
  };
  const plan = projectDeletionPlan(data, own, translate);
  assert.deepEqual([plan.deletesVMs, plan.keepsVMs, plan.setupId], [[], ["base VM mine"], ""]);

  const shared = { ...blog, vm: { ...own.vm, base: "", shared_vm: "hub" } };
  assert.deepEqual(projectDeletionPlan(data, shared, translate).keepsVMs, ["shared VM hub"]);
  const ssh = { ...blog, vm: { ...shared.vm, shared_vm: "me@devbox", integration: "" } };
  assert.deepEqual(projectDeletionPlan(data, ssh, translate).keepsVMs, ["SSH host me@devbox"]);

  // A base an agent set up that Shelley no longer lists as its own stays.
  const managed = { ...blog, vm: { ...own.vm, managed_base_vm: true, base: "made" } };
  assert.deepEqual(projectDeletionPlan(data, managed, translate).keepsVMs, ["base VM made"]);
});

test("projectNameProblem refuses what the server would", () => {
  assert.equal(projectNameProblem("Blog", ["Docs"]), "");
  assert.equal(projectNameProblem("Blog", ["Blog"]), "A project named Blog already exists.");
  assert.match(projectNameProblem("", []), /required/);
  assert.match(projectNameProblem("a/b", []), /\//);
  assert.match(projectNameProblem("a\tb", []), /control/);
  assert.match(projectNameProblem("..", []), /\.\./);
  assert.equal(projectNameProblem("é".repeat(32), []), "");
  assert.match(projectNameProblem("é".repeat(33), []), /64 bytes/);
});

test("a project's view has a path of its own per tab", () => {
  assert.equal(projectViewPath({ id: 4, tab: "page" }), "/project/4");
  assert.equal(projectViewPath({ id: 4, tab: "settings" }), "/project/4/settings");
  assert.deepEqual(projectViewFromPath("/project/4"), { id: 4, tab: "page" });
  assert.deepEqual(projectViewFromPath("/project/4/settings/"), { id: 4, tab: "settings" });
  for (const path of ["/project/", "/project/x", "/project/4/other", "/c/project", "/"])
    assert.equal(projectViewFromPath(path), null, path);
});
