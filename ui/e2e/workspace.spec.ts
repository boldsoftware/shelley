import { test, expect, type Page } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { createConversationViaAPIWithDetails, testWorkingDirectory } from "./helpers";

// These specs exercise workspace mode, which each test turns on before its
// page loads and off again so the worker's other specs see stock Shelley.
test.beforeEach(async ({ request }) => {
  const resp = await request.post("/feature-flags", { data: { name: "workspace", value: true } });
  expect(resp.ok()).toBeTruthy();
});
test.afterEach(async ({ request }) => {
  const resp = await request.delete("/feature-flags", { data: { name: "workspace" } });
  expect(resp.ok()).toBeTruthy();
});

async function openWorkspace(page: Page) {
  if (!(await page.locator(".drawer").isVisible())) {
    await page.getByRole("button", { name: "Open tasks", exact: true }).click();
  } else if (await page.getByRole("button", { name: "Open tasks", exact: true }).isVisible()) {
    await page.getByRole("button", { name: "Open tasks", exact: true }).click();
  }
  await expect(page.getByTestId("assistant-row")).toBeVisible();
}

test("root opens the pinned Assistant; explicit conversation links remain selected", async ({
  page,
  request,
}) => {
  const conversation = await createConversationViaAPIWithDetails(request, "Hello");
  // The worker's earlier specs may have created projects.
  const { projects } = (await (await request.get("/api/workspace")).json()) as {
    projects: unknown[];
  };
  await page.goto("/");
  await expect(page.locator("h1.header-title")).toHaveText("Assistant");
  // The first-run state offers New Project until a project exists.
  await expect(
    page.getByRole("heading", {
      name: projects.length ? "What would you like to get done?" : "Start with a project",
      exact: true,
    }),
  ).toBeVisible();
  await openWorkspace(page);
  await expect(page.getByTestId("assistant-row")).toHaveCount(1);
  await expect(page.locator(".conversation-group").getByTestId("assistant-row")).toHaveCount(0);
  await page.getByRole("button", { name: "Archived tasks", exact: true }).click();
  await expect(page.getByTestId("assistant-row")).toBeVisible();
  await expect(page.getByTestId("assistant-row").getByRole("button")).toHaveCount(0);
  // With a project to move it into, the conversation offers the project picker.
  const project = await request.post("/api/projects", {
    headers: { "X-Shelley-Request": "1" },
    data: { name: `Root ${randomUUID().slice(0, 8)}`, instructions: "", cwd: "", actions: [] },
  });
  expect(project.ok()).toBeTruthy();
  await page.goto(`/c/${conversation.conversationId}`);
  await expect(page.locator("h1.header-title")).not.toHaveText("Assistant");
  await expect(page).toHaveURL(new RegExp(`/c/(${conversation.slug}|${conversation.conversationId})$`));
  await expect(page.getByLabel("Task project", { exact: true })).toBeVisible();
});

test("the Assistant's one URL is /", async ({ page, request }) => {
  const conversation = await createConversationViaAPIWithDetails(request, "Hello");
  const workspace = await request.get("/api/workspace");
  expect(workspace.ok()).toBe(true);
  const { assistant_id: assistantId } = (await workspace.json()) as { assistant_id: string };
  const conversationURL = new RegExp(`/c/(${conversation.slug}|${conversation.conversationId})$`);

  await page.goto(`/c/${assistantId}?message_id=m1`);
  await expect(page.locator("h1.header-title")).toHaveText("Assistant");
  await expect(page).toHaveURL(/\/\?message_id=m1$/);

  await page.goto(`/c/${conversation.conversationId}`);
  await expect(page).toHaveURL(conversationURL);
  await page.goto("/");
  await expect(page.locator("h1.header-title")).toHaveText("Assistant");
  await openWorkspace(page);
  await expect(page.getByTestId("assistant-row")).toHaveAttribute("href", "/");
  await expect(page).toHaveURL(/\/$/);

  await page.evaluate((slug) => {
    window.history.pushState({}, "", `/c/${slug}`);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, conversation.conversationId);
  await expect(page.locator("h1.header-title")).not.toHaveText("Assistant");
  await page.goBack();
  await expect(page.locator("h1.header-title")).toHaveText("Assistant");
  await expect(page).toHaveURL(/\/$/);
  await page.goForward();
  await expect(page.locator("h1.header-title")).not.toHaveText("Assistant");
  await expect(page).toHaveURL(conversationURL);
  await page.goto(`/c/${conversation.conversationId}`);
  await openWorkspace(page);
  await page.getByTestId("assistant-row").click();
  await expect(page.locator("h1.header-title")).toHaveText("Assistant");
  await expect(page).toHaveURL(/\/$/);
});

test("project editor, task creation, failed action retry, and success-only archive", async ({
  page,
}) => {
  // A host without exe.dev integrations (as in CI): the VM choices stay
  // until the editor closes, even after picking this host.
  await page.route("**/api/exe-dev-integrations", (route) =>
    route.fulfill({ json: { integrations: [], default: "" } }),
  );
  await page.goto("/");
  await expect(page.locator("h1.header-title")).toHaveText("Assistant");
  await openWorkspace(page);
  await page.getByRole("button", { name: "New project", exact: true }).click();
  const newProject = page.getByRole("dialog", { name: "New project", exact: true });
  const name = `Publish ${randomUUID().slice(0, 8)}`;
  await newProject.getByLabel("Name", { exact: true }).fill(name);
  // New projects default to their own VMs, in a directory named after them.
  await expect(newProject.getByRole("radio", { name: /Their own VMs/ })).toBeChecked();
  await expect(newProject.getByLabel("Working directory", { exact: true })).toHaveValue(
    `/home/exedev/${name.toLowerCase().replace(" ", "-")}`,
  );
  await newProject.getByRole("radio", { name: /This Shelley host/ }).check();
  await expect(newProject.getByLabel("Working directory", { exact: true })).toHaveValue("");
  await newProject
    .getByLabel("Project instructions (AGENTS.md)", { exact: true })
    .fill("Keep publishing tasks brief.");
  await newProject.getByLabel("Working directory", { exact: true }).fill(testWorkingDirectory());
  await newProject.getByRole("button", { name: "Save project", exact: true }).click();
  await expect(newProject).toBeHidden();

  await page.getByRole("button", { name: `${name} project actions`, exact: true }).click();
  const projectMenu = page.getByTestId("project-actions-menu");
  await expect(projectMenu.getByRole("menuitem")).toHaveText(["Edit project…", "Delete project…"]);
  await expect(projectMenu.getByRole("menuitem", { name: "Delete project…" })).not.toHaveAttribute(
    "aria-disabled",
    "true",
  );
  await projectMenu.getByRole("menuitem", { name: "Edit project…", exact: true }).click();
  const editor = page.getByRole("dialog", { name: "Edit project", exact: true });
  await expect(editor.getByLabel("Project instructions (AGENTS.md)", { exact: true })).toHaveValue(
    "Keep publishing tasks brief.",
  );
  await editor.getByRole("button", { name: "Add action", exact: true }).click();
  await editor.getByLabel("ID", { exact: true }).fill("publish");
  await editor.getByLabel("Button label", { exact: true }).fill("Publish & archive");
  await editor
    .getByLabel("Shell command", { exact: true })
    .fill("printf 'publishing failed\\n'; exit 7");
  await editor
    .getByLabel("Archive task on success (publish/apply actions)", { exact: true })
    .check();
  await editor.getByRole("button", { name: "Save project", exact: true }).click();
  await expect(editor).toBeHidden();
  await page.getByRole("button", { name: `New task in ${name}`, exact: true }).click();
  const creator = page.getByRole("dialog", { name: "New Task", exact: true });
  await expect(creator.getByLabel("Project", { exact: true }).locator("option:checked")).toHaveText(
    name,
  );
  await creator.getByLabel("Title", { exact: false }).fill("Publish release notes");
  await creator.getByLabel("Instructions", { exact: true }).fill("Hello");
  await creator.getByRole("button", { name: "Start task", exact: true }).click();
  await expect(creator).toBeHidden();
  await expect(
    page.getByLabel("Task project", { exact: true }).locator("option:checked"),
  ).toHaveText(name);
  const publish = page.getByRole("button", { name: "Publish & archive", exact: true });
  await expect(publish).toBeEnabled();
  await publish.click();
  const failedRun = page.getByTestId("project-action-card").filter({ hasText: "Failed (exit 7)" });
  await expect(failedRun.getByTestId("project-action-status")).toHaveText("Failed (exit 7)");
  await expect(failedRun).toContainText("publishing failed");
  await expect(page.locator("h1.header-title")).not.toContainText("archived");
  await expect(publish).toBeEnabled();
  // The agent gets the failure to work on, in the run's card.
  await expect(page.getByTestId("message").getByTestId("project-action-card")).toContainText(
    "Sent to the agent",
  );

  // A command that hangs can be stopped.
  await page.getByRole("button", { name: "Edit task project", exact: true }).click();
  const hang = page.getByRole("dialog", { name: "Edit project", exact: true });
  await hang.getByLabel("Shell command", { exact: true }).fill("printf 'waiting\\n'; sleep 600");
  await hang.getByRole("button", { name: "Save project", exact: true }).click();
  await expect(hang).toBeHidden();
  await publish.click();
  const hungRun = page.getByTestId("project-action-card").filter({ hasText: "Running" });
  await expect(hungRun).toContainText("waiting");
  await hungRun.getByTestId("project-action-stop").click();
  const stoppedRun = page
    .getByTestId("project-action-card")
    .filter({ hasText: "Stopped by the user" });
  await expect(stoppedRun.getByTestId("project-action-status")).toHaveText("Interrupted");
  await expect(publish).toBeEnabled();

  await page.getByRole("button", { name: "Edit task project", exact: true }).click();
  const edit = page.getByRole("dialog", { name: "Edit project", exact: true });
  await edit
    .getByLabel("Shell command", { exact: true })
    .fill("printf 'published successfully\\n'");
  await edit.getByRole("button", { name: "Save project", exact: true }).click();
  await expect(edit).toBeHidden();
  await publish.click();
  const successfulRun = page.getByTestId("project-action-card").filter({ hasText: "Succeeded" });
  await expect(successfulRun.getByTestId("project-action-status")).toHaveText("Succeeded");
  await expect(page.locator("h1.header-title")).toContainText("archived");
  await expect(successfulRun).toContainText("published successfully");
  await expect(failedRun).toBeVisible();
  await expect(publish).toBeDisabled();
});

test("agent project edits arrive through workspace SSE without navigating away from Assistant", async ({
  page,
  request,
}) => {
  await page.goto("/");
  await expect(page.locator("h1.header-title")).toHaveText("Assistant");
  await openWorkspace(page);
  const name = `Agent ${randomUUID().slice(0, 8)}`;
  const response = await request.post("/api/projects", {
    headers: { "X-Shelley-Request": "1" },
    data: {
      name,
      instructions: "Created outside this tab",
      cwd: testWorkingDirectory(),
      actions: [],
    },
  });
  expect(response.ok()).toBeTruthy();
  const project = await response.json();
  await expect(
    page.getByRole("button", { name: `New task in ${name}`, exact: true }),
  ).toBeVisible();
  const renamed = `${name} renamed`;
  const update = await request.put(`/api/projects/${encodeURIComponent(name)}`, {
    headers: { "X-Shelley-Request": "1" },
    data: { ...project, name: renamed },
  });
  expect(update.ok()).toBeTruthy();
  await expect(
    page.getByRole("button", { name: `New task in ${renamed}`, exact: true }),
  ).toBeVisible();
  await expect(page.locator("h1.header-title")).toHaveText("Assistant");
});

test("project and conversation ⋯ menus: delete waits for an empty project; move to project", async ({
  page,
  request,
}) => {
  const headers = { "X-Shelley-Request": "1" };
  const project = async (name: string) => {
    const response = await request.post("/api/projects", {
      headers,
      data: { name, instructions: "", cwd: testWorkingDirectory(), actions: [] },
    });
    expect(response.ok()).toBeTruthy();
    return (await response.json()) as { name: string };
  };
  const suffix = randomUUID().slice(0, 8);
  const from = `From ${suffix}`;
  const to = `To ${suffix}`;
  const source = await project(from);
  await project(to);
  const created = await request.post("/api/conversations/new", {
    headers,
    data: { project: source.name, title: `Move me ${suffix}`, message: "Hello" },
  });
  expect(created.ok()).toBeTruthy();
  const { conversation_id: projectConversationId } = (await created.json()) as { conversation_id: string };

  await page.goto("/");
  await openWorkspace(page);
  const fromGroup = page.locator(`section[aria-label="${from}"]`);
  const toGroup = page.locator(`section[aria-label="${to}"]`);
  const conversationRow = page.locator(`.conversation-item[data-conversation-id="${projectConversationId}"]`);
  const fromToggle = fromGroup.locator(".project-toggle");
  const toToggle = toGroup.locator(".project-toggle");
  await expect(fromGroup.locator(conversationRow)).toBeVisible();
  await expect(conversationRow.locator(".working-indicator")).toHaveCount(0);
  // Empty projects start folded.
  await expect(fromToggle).toHaveAttribute("aria-expanded", "true");
  await expect(toToggle).toHaveAttribute("aria-expanded", "false");
  // Fold the empty project explicitly; its first conversation unfolds it anyway.
  await toToggle.click();
  await expect(toToggle).toHaveAttribute("aria-expanded", "true");
  await toToggle.click();
  await expect(toToggle).toHaveAttribute("aria-expanded", "false");

  // A project with conversations can't be deleted, and its menu says why.
  await fromGroup.getByRole("button", { name: `${from} project actions`, exact: true }).click();
  const projectMenu = page.getByTestId("project-actions-menu");
  const remove = projectMenu.getByRole("menuitem", { name: "Delete project…" });
  await expect(remove).toHaveAttribute("aria-disabled", "true");
  await expect(remove.locator("[data-note]")).toHaveAttribute(
    "data-note",
    "Archive or move its active task first",
  );
  await page.keyboard.press("Escape");
  await expect(projectMenu).toHaveCount(0);

  await conversationRow.getByRole("button", { name: "Actions", exact: true }).click();
  const conversationMenu = page.getByTestId("conversation-actions-menu");
  await conversationMenu.getByRole("menuitem", { name: "Move to project" }).click();
  await conversationMenu.getByRole("menuitem", { name: to, exact: true }).click();
  await expect(toGroup.locator(conversationRow)).toBeVisible();
  await expect(fromGroup.locator(conversationRow)).toHaveCount(0);
  // The first conversation unfolds its project; the emptied one folds.
  await expect(toToggle).toHaveAttribute("aria-expanded", "true");
  await expect(fromToggle).toHaveAttribute("aria-expanded", "false");
  // An explicit choice outlasts emptiness.
  await fromToggle.click();
  await expect(fromToggle).toHaveAttribute("aria-expanded", "true");
  await toToggle.click();
  await expect(toGroup.locator(conversationRow)).toHaveCount(0);
  await expect(toToggle).toHaveAttribute("aria-expanded", "false");
});

test("the Assistant shows no host or working directory; conversations still do", async ({
  page,
  request,
}) => {
  const conversation = await createConversationViaAPIWithDetails(request, "Hello");
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto(`/c/${conversation.conversationId}`);
  await expect(page.locator(".status-ready")).toBeVisible();
  await expect(page.locator(".status-readout-cwd")).toBeVisible();

  await page.goto("/");
  await expect(page.locator("h1.header-title")).toHaveText("Assistant");
  await expect(page.locator(".context-usage-label")).toBeVisible();
  await expect(page.locator(".status-ready")).toHaveCount(0);
  await expect(page.locator(".status-readout-cwd, .status-field-cwd")).toHaveCount(0);

  await page.setViewportSize({ width: 390, height: 800 });
  await page.getByRole("button", { name: "More options", exact: true }).click();
  await expect(page.locator(".overflow-menu-item", { hasText: "Change Language" })).toBeVisible();
  await expect(page.locator(".overflow-menu-item", { hasText: "Directory" })).toHaveCount(0);
});
