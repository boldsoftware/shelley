import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import {
  createConversationViaAPIWithDetails,
  openProjectSettings,
  testWorkingDirectory,
} from "./helpers";

// Outside workspace mode (the default), Shelley is stock: no pinned
// Assistant, upstream's drawer, and "Group by → Project" groups
// conversations by the project profile they run with.
test("stock mode offers Group by Project without the workspace", async ({ page, request }) => {
  const name = `Stock ${randomUUID().slice(0, 8)}`;
  const project = await request.post("/api/projects", {
    headers: { "X-Shelley-Request": "1" },
    data: { name, instructions: "", cwd: testWorkingDirectory(), actions: [] },
  });
  expect(project.ok()).toBeTruthy();
  const created = await request.post("/api/conversations/new", {
    headers: { "X-Shelley-Request": "1" },
    data: { project: name, message: "echo: in a project" },
  });
  expect(created.ok()).toBeTruthy();
  const { conversation_id: projectConversationId } = await created.json();
  const plain = await createConversationViaAPIWithDetails(request, "echo: in no project");

  await page.goto("/");
  await expect(page).toHaveURL(/\/c\//);
  await page.getByRole("button", { name: "Open conversations", exact: true }).click();
  await expect(page.getByTestId("assistant-row")).toHaveCount(0);
  await expect(page.getByTestId("nav-tab-dev")).toHaveCount(0);
  await expect(page.locator(".project-group")).toHaveCount(0);

  await page.getByRole("button", { name: "Group conversations" }).click();
  await page
    .locator(".group-by-menu-item")
    .filter({ hasText: /^Project$/ })
    .click();
  const group = page.locator(".project-group", {
    has: page.getByText(name, { exact: true }),
  });
  await expect(group.locator(`[data-conversation-id="${projectConversationId}"]`)).toBeVisible();
  const none = page.getByRole("region", { name: "No project", exact: true });
  await expect(none.locator(`[data-conversation-id="${plain.conversationId}"]`)).toBeVisible();

  // Outside workspace mode a new conversation has no Project picker.
  await page.getByRole("button", { name: `New conversation in ${name}`, exact: true }).click();
  await expect(page.getByTestId("message-input")).toBeVisible();
  await expect(page.locator(".project-picker")).toHaveCount(0);
  await expect(page.locator(".status-field-cwd")).toBeVisible();
  await page.goto("/");
  await page.getByRole("button", { name: "Open conversations", exact: true }).click();

  // The choice is remembered.
  await page.reload();
  await page.getByRole("button", { name: "Open conversations", exact: true }).click();
  await expect(group.locator(`[data-conversation-id="${projectConversationId}"]`)).toBeVisible();

  // Projects run on this host until edited; a host with no exe.dev
  // integration offers only machines reached with ssh. Their own VMs copy a
  // named base VM, start empty, or copy one an agent automanages.
  let integrations: string[] = [];
  await page.route("**/api/exe-dev-integrations", (route) =>
    route.fulfill({ json: { integrations, default: integrations[0] ?? "" } }),
  );
  const openEditor = () => openProjectSettings(page, name);
  const editor = await openEditor();
  await expect(editor.getByLabel("Working directory", { exact: true })).toBeVisible();
  await expect(editor.getByRole("radio", { name: /This Shelley host/ })).toBeChecked();
  await expect(editor.getByRole("radio", { name: /Their own VMs/ })).toHaveCount(0);
  await editor.getByRole("radio", { name: /One shared machine/ }).check();
  await expect(editor.getByLabel("Reached through", { exact: true })).toHaveCount(0);
  await expect(editor.getByLabel("exe.dev integration", { exact: true })).toHaveCount(0);
  await editor.getByLabel("SSH destination", { exact: true }).fill("me@devbox");
  await page.goBack();
  await page.getByRole("button", { name: "Open conversations", exact: true }).click();

  integrations = ["exe"];
  await openEditor();
  await expect(editor.getByRole("radio", { name: /This Shelley host/ })).toBeChecked();
  await expect(editor.getByText("Recommended")).toHaveCount(0);
  // A shared machine is an exe.dev VM through an integration, or ssh.
  await editor.getByRole("radio", { name: /One shared machine/ }).check();
  const via = editor.getByLabel("Reached through", { exact: true });
  await expect(via).toHaveValue("exe");
  await expect(editor.getByLabel("VM", { exact: true })).toBeVisible();
  await via.selectOption({ label: "ssh, with this host's SSH configuration" });
  await expect(editor.getByLabel("SSH destination", { exact: true })).toBeVisible();
  const base = editor.getByLabel("Base VM", { exact: true });
  const automanage = editor.getByLabel(/Automanage/);
  await editor.getByRole("radio", { name: /Their own VMs/ }).check();
  await expect(base).toHaveValue("");
  await expect(base).toHaveAttribute("placeholder", /Optional/);
  await expect(editor.getByText(/starts on a new, empty VM/)).toBeVisible();
  await base.fill("my-base");
  await expect(editor.getByText(/a copy of this existing VM/)).toBeVisible();
  await automanage.check();
  await expect(base).toHaveAttribute("readonly", "");
  await expect(base).toHaveValue("");
  await expect(base).toHaveAttribute("placeholder", /setup task/);
  await expect(editor.getByText(/Saving starts a setup/)).toBeVisible();
  await automanage.uncheck();
  await expect(base).toHaveValue("");
  const saved = page.waitForRequest(
    (r) => r.url().includes("/api/projects/") && r.method() === "PUT",
  );
  await editor.getByRole("button", { name: "Save project", exact: true }).click();
  expect((await saved).postDataJSON().vm).toMatchObject({
    managed_base_vm: false,
    base: "",
    shared_vm: "",
    integration: "exe",
  });
});
