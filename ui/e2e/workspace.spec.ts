import { test, expect, type Page } from "@playwright/test";
import { randomUUID } from "node:crypto";
import {
  createConversationViaAPIWithDetails,
  editProject,
  installServerLinks,
  openProjectSettings,
  testWorkingDirectory,
} from "./helpers";

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
  const workspace = (await (await request.get("/api/workspace")).json()) as {
    projects: { name: string }[];
    shelley_project: string;
  };
  const firstRun = workspace.projects.every((p) => p.name === workspace.shelley_project);
  await page.goto("/");
  await expect(page.locator("h1.header-title")).toHaveText("Assistant");
  await expect(page).toHaveTitle(/^Assistant - /);
  // The first-run state offers ways to start until a project exists.
  await expect(
    page.getByRole("heading", {
      name: "What would you like to get done?",
      exact: true,
    }),
  ).toBeVisible();
  await expect(page.getByTestId("onboarding-web-app")).toHaveCount(firstRun ? 1 : 0);
  await openWorkspace(page);
  await expect(page.getByTestId("assistant-row")).toHaveCount(1);
  await expect(page.locator(".conversation-group").getByTestId("assistant-row")).toHaveCount(0);
  // Archived tasks are behind search's Archived toggle, listed with the
  // active ones; the footer button is stock Shelley's.
  await expect(page.getByRole("button", { name: "View Archived", exact: true })).toHaveCount(0);
  const archived = await createConversationViaAPIWithDetails(request, "Hello");
  expect(
    (
      await request.post(`/api/conversation/${archived.conversationId}/archive`, {
        headers: { "X-Shelley-Request": "1" },
      })
    ).ok(),
  ).toBe(true);
  const row = (id: string) => page.locator(`.conversation-item[data-conversation-id="${id}"]`);
  await expect(row(conversation.conversationId)).toBeVisible();
  await expect(row(archived.conversationId)).toHaveCount(0);
  await page.getByRole("button", { name: "Search tasks...", exact: true }).click();
  const toggle = page.getByTestId("archived-toggle");
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-pressed", "true");
  await expect(row(conversation.conversationId)).toBeVisible();
  await expect(row(archived.conversationId)).toHaveClass(/conversation-item-archived/);
  await expect(page.getByTestId("assistant-row")).toBeVisible();
  await expect(page.getByTestId("assistant-row").getByRole("button")).toHaveCount(0);
  await row(archived.conversationId).click({ button: "right" });
  await page.getByRole("menuitem", { name: "Restore", exact: true }).click();
  await expect(row(archived.conversationId)).not.toHaveClass(/conversation-item-archived/);
  await toggle.click();
  await expect(row(archived.conversationId)).toBeVisible();
  await page.goto(`/c/${conversation.conversationId}`);
  await expect(page.locator("h1.header-title")).not.toHaveText("Assistant");
  await expect(page).toHaveURL(
    new RegExp(`/c/(${conversation.slug}|${conversation.conversationId})$`),
  );
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
  const name = `Publish ${randomUUID().slice(0, 8)}`;
  const nameField = page.getByTestId("new-project-row").getByLabel("New project", { exact: true });
  await nameField.fill(name);
  await nameField.press("Enter");
  await expect(nameField).toHaveCount(0);

  // A project without a page opens on its settings, which can delete it.
  const editor = await openProjectSettings(page, name);
  await expect(page).toHaveURL(/\/project\/\d+\/settings$/);
  await expect(editor.getByRole("button", { name: "Delete project…" })).toBeEnabled();
  await editor
    .getByLabel("Project instructions (AGENTS.md)", { exact: true })
    .fill("Keep publishing tasks brief.");
  await editor.getByLabel("Working directory", { exact: true }).fill(testWorkingDirectory());
  // A page gives the view a tab framing it, first.
  await editor.getByLabel("Project page", { exact: true }).fill("https://example.com/");
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
  await expect(editor.getByText("Saved.", { exact: true })).toBeVisible();
  const view = page.getByTestId("project-view");
  await expect(view.getByRole("navigation").getByRole("button")).toHaveText([
    "example.com",
    "Settings",
  ]);
  await view.getByRole("button", { name: "example.com", exact: true }).click();
  await expect(page).toHaveURL(/\/project\/\d+$/);
  await expect(editor).toBeHidden();
  // The bar links to the page, as does the frame when it can't show it.
  await expect(
    view.locator(".header").getByRole("link", { name: "Open example.com in a new tab" }),
  ).toHaveAttribute("href", "https://example.com/");
  await page.goBack();
  await expect(editor).toBeVisible();
  await openWorkspace(page);
  await page.getByRole("button", { name: `New task in ${name}`, exact: true }).click();
  // The regular composer starts it, with the project picked.
  await expect(page.locator(".project-picker")).toHaveText(name);
  await page.getByTestId("message-input").fill("Hello");
  await page.getByRole("button", { name: "Send message", exact: true }).click();
  await expect(page).toHaveURL(/\/c\//);
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
  await editProject(page, name, (editor) =>
    editor.getByLabel("Shell command", { exact: true }).fill("printf 'waiting\\n'; sleep 600"),
  );
  await publish.click();
  const hungRun = page.getByTestId("project-action-card").filter({ hasText: "Running" });
  await expect(hungRun).toContainText("waiting");
  await hungRun.getByTestId("project-action-stop").click();
  const stoppedRun = page
    .getByTestId("project-action-card")
    .filter({ hasText: "Stopped by the user" });
  await expect(stoppedRun.getByTestId("project-action-status")).toHaveText("Interrupted");
  await expect(publish).toBeEnabled();

  await editProject(page, name, (editor) =>
    editor.getByLabel("Shell command", { exact: true }).fill("printf 'published successfully\\n'"),
  );
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
  const { conversation_id: projectConversationId } = (await created.json()) as {
    conversation_id: string;
  };

  await page.goto("/");
  await openWorkspace(page);
  const fromGroup = page.locator(`section[aria-label="${from}"]`);
  const toGroup = page.locator(`section[aria-label="${to}"]`);
  const conversationRow = page.locator(
    `.conversation-item[data-conversation-id="${projectConversationId}"]`,
  );
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

  // A project with conversations can't be deleted, and its settings say why.
  const editor = await openProjectSettings(page, from);
  await expect(editor.getByRole("button", { name: "Delete project…" })).toBeDisabled();
  await expect(editor).toContainText("Archive or move its active task first");
  await page.goBack();
  await openWorkspace(page);

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

test("project folds survive reloads and renames; a deleted project's goes", async ({
  page,
  request,
}) => {
  const headers = { "X-Shelley-Request": "1" };
  const suffix = randomUUID().slice(0, 8);
  const body = (name: string) => ({
    name,
    instructions: "",
    cwd: testWorkingDirectory(),
    actions: [],
    repos: [],
    vm: null,
  });
  const project = async (name: string) => {
    const response = await request.post("/api/projects", { headers, data: body(name) });
    expect(response.ok()).toBeTruthy();
    return (await response.json()) as { id: number; name: string };
  };
  const busy = await project(`Busy ${suffix}`);
  const empty = await project(`Empty ${suffix}`);
  const created = await request.post("/api/conversations/new", {
    headers,
    data: { project: busy.name, title: `Fold me ${suffix}`, message: "Hello" },
  });
  expect(created.ok()).toBeTruthy();

  await page.goto("/");
  await openWorkspace(page);
  const toggle = (name: string) => page.locator(`section[aria-label="${name}"] .project-toggle`);
  await expect(toggle(busy.name)).toHaveAttribute("aria-expanded", "true");
  await expect(toggle(empty.name)).toHaveAttribute("aria-expanded", "false");
  await toggle(busy.name).click();
  await toggle(empty.name).click();
  await expect(toggle(busy.name)).toHaveAttribute("aria-expanded", "false");
  await expect(toggle(empty.name)).toHaveAttribute("aria-expanded", "true");

  await page.reload();
  await openWorkspace(page);
  await expect(toggle(busy.name)).toHaveAttribute("aria-expanded", "false");
  await expect(toggle(empty.name)).toHaveAttribute("aria-expanded", "true");

  // Move up swaps a project with the one before it, for every list of projects.
  const order = async () =>
    (
      (await (await request.get("/api/workspace")).json()) as { projects: { name: string }[] }
    ).projects.map((p) => p.name);
  const before = await order();
  const at = before.indexOf(empty.name);
  const settings = await openProjectSettings(page, empty.name);
  await settings.getByRole("button", { name: "Move up", exact: true }).click();
  const after = [...before];
  [after[at - 1], after[at]] = [after[at], after[at - 1]];
  await expect.poll(order).toEqual(after);
  const swapped = [after[at - 1], after[at]];
  await expect
    .poll(async () =>
      (
        await page
          .locator("section.project-group")
          .evaluateAll((sections) => sections.map((s) => s.getAttribute("aria-label")))
      ).filter((label) => label !== null && swapped.includes(label)),
    )
    .toEqual(swapped);

  // Folds follow the project's id, not its name.
  const renamed = `Renamed ${suffix}`;
  const rename = await request.put(`/api/projects/${encodeURIComponent(busy.name)}`, {
    headers,
    data: body(renamed),
  });
  expect(rename.ok()).toBeTruthy();
  const remove = await request.delete(`/api/projects/${encodeURIComponent(empty.name)}`, {
    headers,
  });
  expect(remove.ok()).toBeTruthy();
  await page.reload();
  await openWorkspace(page);
  await expect(toggle(renamed)).toHaveAttribute("aria-expanded", "false");
  await expect(toggle(empty.name)).toHaveCount(0);
  const folds = await page.evaluate(() => localStorage.getItem("shelley-project-folds"));
  const stored = JSON.parse(folds ?? "{}") as Record<string, boolean>;
  expect(stored[`project:${busy.id}`]).toBe(true);
  expect(`project:${empty.id}` in stored).toBe(false);
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

test("a task's title row holds its page tabs and where it runs; actions get the only other row", async ({
  page,
  request,
}) => {
  const name = `Header ${randomUUID().slice(0, 8)}`;
  const created = await request.post("/api/projects", {
    data: {
      name,
      instructions: "",
      cwd: testWorkingDirectory(),
      actions: [{ id: "ship", label: "Ship it", command: "true" }],
    },
  });
  expect(created.ok()).toBeTruthy();
  const start = async (project: string, message: string) => {
    const response = await request.post("/api/conversations/new", {
      data: { project, title: `Header task ${randomUUID().slice(0, 8)}`, message },
    });
    expect(response.ok()).toBeTruthy();
    return ((await response.json()) as { conversation_id: string }).conversation_id;
  };
  const links = [1, 2, 3, 4]
    .map((n) => `[Page ${n}](https://page${n}.example.com/ "tab")`)
    .join(" ");
  const id = await start(name, `echo: See ${links}.`);
  await installServerLinks(page);
  // Its VM, as the server would report a shared one.
  await page.route("**/api/workspace", async (route) => {
    const workspace = await (await route.fetch()).json();
    workspace.vms.push({
      conversation_id: id,
      vm_name: "exe-hub",
      base_vm: "",
      integration: "hub",
      shared: true,
      status: "ready",
      error: "",
      ssh_dest: "",
      https_url: "",
      dir: "/home/exedev/hub",
      conversation_archived: false,
      replaced: false,
    });
    await route.fulfill({ json: workspace });
  });
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto(`/c/${id}`);
  const header = page.locator(".header");
  // Its VM's site and the pages the agent linked.
  await expect(header.getByTestId("page-tab")).toHaveCount(5);
  await expect(header.getByRole("button", { name: "All tabs" })).toHaveCount(0);
  const place = header.getByTestId("conversation-place");
  await expect(place).toHaveAccessibleName("Shared VM exe-hub · connected");
  await place.click();
  const details = page.locator(".conversation-place-popover");
  await expect(details).toContainText("Works on shared VM exe-hub");
  await expect(details.getByRole("status")).toHaveText("connected");
  await expect(details.getByRole("link", { name: /exe-hub/ })).toHaveAttribute(
    "href",
    "https://exe-hub.exe.xyz/",
  );
  await page.keyboard.press("Escape");
  // The project's actions are its second row.
  const projectRow = page.locator(".project-header");
  await expect(projectRow.getByRole("button", { name: "Ship it", exact: true })).toBeVisible();
  await expect(projectRow).not.toContainText("exe-hub");
  // Tabs show the pages; the host's link stays in the menu, not the header.
  await expect(header.getByRole("link", { name: "vm.example", includeHidden: true })).toHaveCount(
    0,
  );
  await expect(header.locator(".chat-workspace-divider")).toHaveCount(0);
  await header.getByRole("button", { name: "More options", exact: true }).click();
  await expect(page.locator(".overflow-menu-item", { hasText: "vm.example" })).toBeVisible();
  await page.keyboard.press("Escape");

  // On a phone the title gives way; tabs that don't fit are in a menu.
  await page.setViewportSize({ width: 390, height: 800 });
  await expect(place).toBeInViewport({ ratio: 1 });
  await header.getByRole("button", { name: "All tabs" }).click();
  const menu = page.getByTestId("page-tabs-menu");
  await expect(menu.getByRole("menuitem")).toHaveText([
    "Chat",
    "exe-hub",
    "Page 1",
    "Page 2",
    "Page 3",
    "Page 4",
  ]);
  await page.keyboard.press("Escape");

  // A task on this host without project machinery has one row.
  await page.setViewportSize({ width: 1280, height: 800 });
  const bare = `${name} bare`;
  expect(
    (
      await request.post("/api/projects", {
        data: { name: bare, instructions: "", cwd: testWorkingDirectory() },
      })
    ).ok(),
  ).toBeTruthy();
  const plain = await start(bare, "Hello");
  await page.goto(`/c/${plain}`);
  await expect(header.getByTestId("conversation-place")).toHaveAccessibleName(/^This host, /);
  await expect(page.locator(".project-header")).toHaveCount(0);
  await page.goto("/");
  await expect(page.locator("h1.header-title")).toHaveText("Assistant");
  await expect(page.getByTestId("conversation-place")).toHaveCount(0);
  await expect(page.locator(".project-header")).toHaveCount(0);
});

test("a task moves to another machine from where it runs, and back", async ({ page, request }) => {
  const name = `Mover ${randomUUID().slice(0, 8)}`;
  expect(
    (
      await request.post("/api/projects", {
        data: { name, instructions: "", cwd: testWorkingDirectory() },
      })
    ).ok(),
  ).toBeTruthy();
  const started = await request.post("/api/conversations/new", {
    data: { project: name, title: `Mover task ${randomUUID().slice(0, 8)}`, message: "Hello" },
  });
  expect(started.ok()).toBeTruthy();
  const { conversation_id: id } = (await started.json()) as { conversation_id: string };
  await page.goto(`/c/${id}`);
  const place = page.locator(".header").getByTestId("conversation-place");
  await expect(place).toHaveAccessibleName(/^This host/);
  await expect(page.getByText("Hello", { exact: true })).toBeVisible();
  await expect(page.getByTestId("agent-thinking")).toHaveCount(0);

  await place.click();
  await page.getByTestId("conversation-place-move").click();
  const dialog = page.getByRole("dialog", { name: "Move task to" });
  await dialog.getByText("An existing machine").click();
  const via = dialog.locator("#machine-via");
  if (await via.count()) await via.selectOption("");
  await dialog.getByLabel("SSH destination").fill("nobody@shelley-e2e.invalid");
  await dialog.getByLabel("Directory").fill("/tmp");
  await dialog.getByTestId("machine-save").click();
  await expect(dialog).toHaveCount(0);
  await expect(place).toHaveAccessibleName(/^Shared VM nobody@shelley-e2e\.invalid/);
  // The agent is told, as a status line.
  await expect(page.getByTestId("message-cwdchange").last()).toContainText(
    "nobody@shelley-e2e.invalid",
  );

  await place.click();
  await page.getByTestId("conversation-place-move").click();
  await dialog.getByText("This Shelley host").click();
  await dialog.getByTestId("machine-save").click();
  await expect(dialog).toHaveCount(0);
  await expect(place).toHaveAccessibleName(/^This host/);
});
