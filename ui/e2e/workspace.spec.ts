import { test, expect, type Page } from "@playwright/test";
import { randomUUID } from "node:crypto";
import {
  clearConversationQuery,
  createConversationViaAPIWithDetails,
  fillConversationQuery,
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
  // An off-canvas mobile drawer still counts as visible; open it explicitly.
  // isVisible() doesn't wait, so retry until the page has rendered its
  // header (and with it the button, on narrow screens) or the drawer.
  const open = page.getByRole("button", { name: "Open tasks", exact: true });
  const row = page.getByTestId("assistant-row");
  await expect(async () => {
    if (await open.isVisible()) await open.click();
    await expect(row).toBeInViewport({ timeout: 1000 });
  }).toPass();
}

test("the dev-prod-tabs flag adds a prod tab that links to exe.dev integration setup", async ({
  page,
  request,
}) => {
  const on = await request.post("/feature-flags", { data: { name: "dev-prod-tabs", value: true } });
  expect(on.ok()).toBeTruthy();
  try {
    // A host without an exe.dev integration (as in CI).
    const setupUrl =
      "https://exe.dev/integrations/add?service=exe-dev&attach=vm%3Ahost&source=shelley";
    await page.route("**/api/exe-vms", (route) =>
      route.fulfill({ json: { vms: [], setup_url: setupUrl } }),
    );
    await page.goto("/");
    await openWorkspace(page);
    // prod keeps the pinned Assistant above its VM list.
    await page.getByTestId("nav-tab-prod").click();
    await expect(page.getByTestId("assistant-row")).toBeVisible();
    await expect(page.getByTestId("assistant-row")).toHaveAttribute("href", "/");
    await expect(page.getByTestId("assistant-row").getByRole("button")).toHaveCount(0);
    const link = page.getByRole("link", { name: "Set up an exe.dev integration" });
    await expect(link).toHaveAttribute("href", setupUrl);
    await expect(page.getByText("No VMs.")).toHaveCount(0);
    await page.getByTestId("nav-tab-dev").click();
  } finally {
    const off = await request.delete("/feature-flags", { data: { name: "dev-prod-tabs" } });
    expect(off.ok()).toBeTruthy();
  }
});

test("the onboarding card tops the Assistant's transcript until dismissed", async ({
  page,
  request,
}) => {
  // Shown whatever an earlier run left, until the dismissal under test.
  await page.route("**/api/workspace", async (route) => {
    const response = await route.fetch();
    await route.fulfill({
      response,
      json: { ...(await response.json()), onboarding_dismissed: false },
    });
  });
  await page.route("**/api/onboarding", (route) =>
    route.fulfill({
      json: {
        text_number: "",
        text_connected: false,
        exe_dev: "acme-exe",
        vm_host: "NAME.exe.test",
      },
    }),
  );
  await page.goto("/");
  const card = page.getByTestId("onboarding");
  await expect(card).toBeVisible();
  await page.screenshot({ path: "test-results/onboarding-card.png" });
  // Build a web app prefills a prompt for the user to finish.
  await card.getByTestId("onboarding-web-app").click();
  const input = page.getByTestId("message-input");
  await expect(input).toHaveValue(/through the acme-exe integration/);
  await expect(input).toHaveValue(/make it public with `share set-public NAME`.*\n\nThe app: $/s);
  // Sent messages land below the card, which stays in the transcript.
  const text = `echo: onboarding ${randomUUID().slice(0, 8)}`;
  await input.fill(text);
  await page.getByTestId("send-button").click();
  const sent = page.locator(".messages-list").getByText(text).first();
  await expect(sent).toBeVisible();
  await expect(card).toBeAttached();
  expect(
    await card.evaluate(
      (el, t) =>
        [...document.querySelectorAll(".messages-list *")].some(
          (m) =>
            m.textContent === t &&
            !!(el.compareDocumentPosition(m) & Node.DOCUMENT_POSITION_FOLLOWING),
        ),
      text,
    ),
  ).toBe(true);
  await card.scrollIntoViewIfNeeded();
  await page.screenshot({ path: "test-results/onboarding-card-messages.png" });
  // Dismissing is the server's, so it survives reloads.
  await page.unrouteAll({ behavior: "wait" });
  await card.getByTestId("onboarding-dismiss").click();
  await expect(card).toHaveCount(0);
  await page.reload();
  await expect(page.locator(".messages-list").getByText(text).first()).toBeVisible();
  await expect(page.getByTestId("onboarding")).toHaveCount(0);
  await page.screenshot({ path: "test-results/onboarding-dismissed.png" });
  const workspace = await (await request.get("/api/workspace")).json();
  expect(workspace.onboarding_dismissed).toBe(true);
});

test("root opens the pinned Assistant; explicit conversation links remain selected", async ({
  page,
  request,
}) => {
  const conversation = await createConversationViaAPIWithDetails(request, "Hello");
  // The worker's earlier specs may have created projects.
  const workspace = (await (await request.get("/api/workspace")).json()) as {
    onboarding_dismissed: boolean;
  };
  await page.goto("/");
  await expect(page.locator("h1.header-title")).toHaveText("Assistant");
  await expect(page).toHaveTitle(/^Assistant - /);
  // The onboarding card offers ways to start until the user dismisses it.
  await expect(
    page.getByRole("heading", {
      name: "What would you like to get done?",
      exact: true,
    }),
  ).toBeVisible();
  await expect(page.getByTestId("onboarding-web-app")).toHaveCount(
    workspace.onboarding_dismissed ? 0 : 1,
  );
  await openWorkspace(page);
  await expect(page.getByTestId("assistant-row")).toHaveCount(1);
  await expect(page.locator(".conversation-group").getByTestId("assistant-row")).toHaveCount(0);
  // The dev/prod tabs need their own flag.
  await expect(page.getByTestId("nav-tab-prod")).toHaveCount(0);
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
  // An archived row's Restore and Delete take its Archive's place; a touch
  // screen shows them on the open row.
  const restore = row(archived.conversationId).getByRole("button", { name: "Restore" });
  await expect(restore).toBeHidden();
  await row(archived.conversationId).click();
  await openWorkspace(page);
  await expect(row(archived.conversationId).getByRole("button", { name: "Delete" })).toBeVisible();
  await restore.click();
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
    .getByLabel("AGENTS.md (instructions used by every project task)", { exact: true })
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

test("a project's + composes its new conversation there, in the drawer and on the server", async ({
  page,
  request,
}) => {
  const headers = { "X-Shelley-Request": "1" };
  const suffix = randomUUID().slice(0, 8);
  const [a, b] = [`Plus A ${suffix}`, `Plus B ${suffix}`];
  for (const name of [a, b]) {
    const response = await request.post("/api/projects", {
      headers,
      data: { name, instructions: "", cwd: testWorkingDirectory(), actions: [] },
    });
    expect(response.ok()).toBeTruthy();
  }
  await page.goto("/");
  await openWorkspace(page);
  const composerRow = (project: string) =>
    page.locator(`section[aria-label="${project}"] [data-conversation-id="composer"]`);
  // Phones, which can't hover, always show a project's +.
  const plus = page.getByRole("button", { name: `New task in ${a}`, exact: true });
  await expect(plus).toHaveCSS("opacity", "1");
  await plus.click();
  await expect(page.locator(".project-picker")).toHaveText(a);
  await openWorkspace(page);
  await expect(composerRow(a)).toBeVisible();
  // Another project's + moves the new conversation there.
  await page.getByRole("button", { name: `New task in ${b}`, exact: true }).click();
  await expect(page.locator(".project-picker")).toHaveText(b);
  await openWorkspace(page);
  await expect(composerRow(b)).toBeVisible();
  await expect(composerRow(a)).toHaveCount(0);
  await page.getByRole("button", { name: "Close tasks", exact: true }).click();
  // New Task from its composer stays in the project.
  await page.getByRole("button", { name: "New Task", exact: true }).click();
  await expect(page.locator(".project-picker")).toHaveText(b);

  await page.getByTestId("message-input").fill("Hello");
  await page.getByRole("button", { name: "Send message", exact: true }).click();
  await expect(page).toHaveURL(/\/c\//);
  await openWorkspace(page);
  const id = await page
    .locator(`section[aria-label="${b}"] .conversation-item[aria-current="page"]`)
    .getAttribute("data-conversation-id");
  const workspace = await (await request.get("/api/workspace", { headers })).json();
  expect(
    workspace.conversations.find((c: { conversation_id: string }) => c.conversation_id === id)
      ?.project,
  ).toBe(b);
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

test("a project's ⋯ menu: delete waits for an empty project; a moved task changes groups", async ({
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

  // Tasks move through the API (the Assistant), not the sidebar.
  const moved = await request.put(`/api/conversation/${projectConversationId}/project`, {
    headers,
    data: { project: to },
  });
  expect(moved.ok()).toBeTruthy();
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

test("a search shows matches in folded projects without unfolding them", async ({
  page,
  request,
}) => {
  const headers = { "X-Shelley-Request": "1" };
  const word = `foldsearch${randomUUID().slice(0, 8)}`;
  const response = await request.post("/api/projects", {
    headers,
    data: {
      name: `Folded ${word}`,
      instructions: "",
      cwd: testWorkingDirectory(),
      actions: [],
      repos: [],
      vm: null,
    },
  });
  expect(response.ok()).toBeTruthy();
  const project = (await response.json()) as { id: number; name: string };
  const created = await request.post("/api/conversations/new", {
    headers,
    data: { project: project.name, title: `Find me ${word}`, message: `echo: ${word}` },
  });
  expect(created.ok()).toBeTruthy();
  const { conversation_id: id } = (await created.json()) as { conversation_id: string };

  await page.goto("/");
  await openWorkspace(page);
  const toggle = page.locator(`section[aria-label="${project.name}"] .project-toggle`);
  const row = page.locator(`[data-conversation-id="${id}"]`);
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect(row).toHaveCount(0);
  const saved = () => page.evaluate(() => localStorage.getItem("shelley-project-folds"));
  const folds = await saved();
  expect(JSON.parse(folds!)[`project:${project.id}`]).toBe(true);

  const search = page.getByTestId("conversation-query-editor");
  await page.getByRole("button", { name: "Search tasks..." }).click();
  // Projects, not tags, organize tasks: no tag filter, nor `tag:` suggestions.
  await expect(page.getByTestId("archived-toggle")).toBeVisible();
  await expect(page.getByRole("button", { name: "Add tag filter" })).toHaveCount(0);
  await fillConversationQuery(search, "tag:");
  await expect(page.getByTestId("tag-filter-panel")).toHaveCount(0);
  await fillConversationQuery(search, word);
  await expect(row).toBeVisible();
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  // Folding during a search lasts only as long as the search.
  await toggle.click();
  await expect(row).toHaveCount(0);
  await toggle.click();
  await expect(row).toBeVisible();
  expect(await saved()).toBe(folds);

  // A reload mid-search leaves the saved fold as it was.
  await page.reload();
  await openWorkspace(page);
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  expect(await saved()).toBe(folds);

  await page.getByRole("button", { name: "Search tasks..." }).click();
  await fillConversationQuery(search, word);
  await expect(row).toBeVisible();
  await clearConversationQuery(search);
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect(row).toHaveCount(0);
  expect(await saved()).toBe(folds);
});

test("the Assistant shows no title, host, working directory, diffs, git graph or message actions; conversations still do", async ({
  page,
  request,
}) => {
  const conversation = await createConversationViaAPIWithDetails(request, "Hello");
  const actions = page.locator(".chat-workspace-actions");
  const gitButtons = actions.getByRole("button", { name: /^(Diffs|Git Graph)$/ });
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto(`/c/${conversation.conversationId}`);
  await expect(page.locator(".status-ready")).toBeVisible();
  await expect(page.locator(".status-readout-cwd")).toBeVisible();
  await expect(gitButtons).toHaveCount(2);
  const title = async () => (await page.locator("h1.header-title").boundingBox())!;
  const header = async () => (await page.locator(".header").boundingBox())!;
  const actionsBox = async () => (await page.locator(".header-actions").boundingBox())!;
  expect((await title()).width).toBeGreaterThan(1);
  const taskHeader = await header();
  const taskActionsRight = (await actionsBox()).x + (await actionsBox()).width;
  const brandRule = async (width: string) => {
    await page.getByRole("button", { name: "Collapse sidebar", exact: true }).click();
    await expect(page.locator(".header-left .exe-brand")).toHaveCSS("border-right-width", width);
    await page.getByRole("button", { name: "Expand sidebar", exact: true }).click();
  };
  await brandRule("1px");
  const hello = page.getByTestId("message").filter({ hasText: "Hello" }).first();
  await hello.hover();
  await expect(hello.getByRole("button", { name: "Copy" })).toBeVisible();

  await page.goto("/");
  // The title row shows no "Assistant", which stays a heading for screen
  // readers, and the header keeps its height and right-aligned actions.
  await expect(page.locator("h1.header-title")).toHaveText("Assistant");
  await expect(page.locator("h1.header-title")).toHaveClass(/\bsr-only\b/);
  expect((await title()).width).toBeLessThanOrEqual(1);
  expect((await header()).height).toBe(taskHeader.height);
  expect((await actionsBox()).x + (await actionsBox()).width).toBe(taskActionsRight);
  // With the drawer collapsed, the logo has no title to rule off.
  await brandRule("0px");
  await expect(page.locator(".context-usage-label")).toBeVisible();
  await expect(page.locator(".status-ready")).toHaveCount(0);
  await expect(page.locator(".status-readout-cwd, .status-field-cwd")).toHaveCount(0);
  await expect(actions.getByRole("button", { name: "Terminal", exact: true })).toBeVisible();
  await expect(gitButtons).toHaveCount(0);
  const text = `texted ${randomUUID()}`;
  const texted = await request.post(`/api/debug/channels/debug-${randomUUID()}/receive`, {
    headers: { "X-Shelley-Request": "1" },
    data: { text },
  });
  expect(texted.ok()).toBeTruthy();
  const message = page.getByTestId("message").filter({ hasText: text });
  await message.hover();
  await message.click();
  await expect(message.getByTestId("message-via-text")).toBeVisible();
  await expect(page.locator(".message-action-bar")).toHaveCount(0);

  await page.setViewportSize({ width: 390, height: 800 });
  await page.getByRole("button", { name: "More options", exact: true }).click();
  await expect(page.locator(".overflow-toolbar-action[aria-label='Terminal']")).toBeVisible();
  await expect(
    page.locator(
      ".overflow-toolbar-action[aria-label='Diffs'], .overflow-toolbar-action[aria-label='Git Graph']",
    ),
  ).toHaveCount(0);
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

test("workspace mode's sidebar rows are their title alone, unless compact-nav is off", async ({
  page,
  request,
}) => {
  const source = await createConversationViaAPIWithDetails(request, "Hello", {
    conversationOptions: { tool_overrides: { message_user: "on" } },
  });
  const fork = await request.post(`/api/conversation/${source.conversationId}/fork`, { data: {} });
  expect(fork.ok()).toBeTruthy();
  const { conversation_id: conversationId } = await fork.json();
  const row = page.locator(`.conversation-item[data-conversation-id="${conversationId}"]`);
  const assistant = page.getByTestId("assistant-row");
  const build = await request.post(`/api/conversation/${conversationId}/customization`, {
    headers: { "X-Shelley-Request": "1" },
    data: { status: "installed", commit: "1" },
  });
  expect(build.ok()).toBeTruthy();
  await page.goto("/");
  await openWorkspace(page);
  await expect(row.locator(".conversation-title")).toBeVisible();
  const title = row.locator(".conversation-title");
  await expect(title).not.toHaveCSS("font-weight", "700");
  const delivered = await request.post(`/api/conversation/${conversationId}/chat`, {
    data: { message: 'message_user: {"text":"Ready","end_turn":true}', model: "predictable" },
  });
  expect(delivered.ok()).toBeTruthy();
  // Unread, its title is bold, like an unread email; no count.
  await expect(title).toHaveCSS("font-weight", "700");
  await expect(row.getByText("1", { exact: true })).toHaveCount(0);
  // Its age shows at the right; a touch screen shows Archive beside it
  // only on the open row.
  const time = row.locator(".conversation-time");
  await expect(time).toHaveText("now");
  await expect(row.getByRole("button", { name: "Archive", exact: true })).toBeHidden();
  await expect(row.getByRole("button", { name: "Actions" })).toHaveCount(0);
  // A task's build status is a label on its title line.
  await expect(row.getByRole("status", { name: "Shelley build" })).toHaveText("Installed");
  await expect(row.locator(".conversation-preview, .conversation-meta")).toHaveCount(0);
  await expect(assistant.locator(".conversation-preview")).toHaveCount(0);
  const short = (await row.boundingBox())!.height;
  // Titles line up with their project's name, past its chevron.
  const label = page.locator(".project-group", { has: row }).locator(".conversation-group-label");
  const titleX = (await row.locator(".conversation-title").boundingBox())!.x;
  expect(Math.abs(titleX - (await label.boundingBox())!.x)).toBeLessThan(1);

  const off = await request.post("/feature-flags", { data: { name: "compact-nav", value: false } });
  expect(off.ok()).toBeTruthy();
  try {
    await page.reload();
    await openWorkspace(page);
    await expect(row.locator(".conversation-preview")).toHaveCount(1);
    await expect(title).toHaveCSS("font-weight", "700");
    await expect(row.locator(".conversation-meta")).toHaveCount(1);
    await expect(assistant.locator(".conversation-preview")).toHaveCount(1);
    expect((await row.boundingBox())!.height).toBeGreaterThan(short * 1.5);
  } finally {
    const reset = await request.delete("/feature-flags", { data: { name: "compact-nav" } });
    expect(reset.ok()).toBeTruthy();
  }
});

for (const workspace of [true, false]) {
  test(`a row turns bold, unread, only once its agent stops working in ${workspace ? "workspace mode" : "stock Shelley"}`, async ({
    page,
    request,
  }) => {
    if (!workspace) {
      const off = await request.post("/feature-flags", {
        data: { name: "workspace", value: false },
      });
      expect(off.ok()).toBeTruthy();
      await page.setViewportSize({ width: 1280, height: 900 });
    }
    const { conversationId } = await createConversationViaAPIWithDetails(request, "Hello", {
      conversationOptions: { tool_overrides: { message_user: "on" } },
    });
    const row = page.locator(`.conversation-item[data-conversation-id="${conversationId}"]`);
    const bold = page.locator(
      `.conversation-item-unread[data-conversation-id="${conversationId}"]`,
    );
    await page.goto(workspace ? "/" : "/new");
    if (workspace) await openWorkspace(page);
    await expect(row).toBeVisible();
    const title = row.locator(".conversation-title");
    const chat = (message: string) =>
      request.post(`/api/conversation/${conversationId}/chat`, {
        data: { message, model: "predictable" },
      });
    expect((await chat('message_user: {"text":"Ready","end_turn":true}')).ok()).toBeTruthy();
    await expect(bold).toBeVisible();
    await expect(title).toHaveCSS("font-weight", "700");
    expect((await chat("delay: 3")).ok()).toBeTruthy();
    await expect(row.locator(".working-indicator")).toBeVisible();
    await expect(bold).toHaveCount(0);
    await expect(title).not.toHaveCSS("font-weight", "700");
    await expect(row.locator(".working-indicator")).toHaveCount(0, { timeout: 15000 });
    await expect(bold).toBeVisible();
    await expect(title).toHaveCSS("font-weight", "700");
    await row.click();
    await expect(row).toHaveAttribute("aria-current", "page");
    await expect(bold).toHaveCount(0);
    await expect(title).not.toHaveCSS("font-weight", "700");
  });
}

test("opening a chat clears its bold title across browsers and keeps the open row quiet", async ({
  page,
  browser,
  baseURL,
  request,
}) => {
  const { conversationId } = await createConversationViaAPIWithDetails(
    request,
    "echo: first visit",
  );
  const observer = await browser.newPage({ baseURL, viewport: { width: 1280, height: 900 } });
  const row = `[data-conversation-id="${conversationId}"]`;
  const bold = observer.locator(`.conversation-item-unread${row}`);
  try {
    await observer.goto("/new");
    await expect(observer.locator(row)).toBeVisible();
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.goto("/new");
    await expect(page.locator(row)).toBeVisible();

    const reply = await request.post(`/api/conversation/${conversationId}/chat`, {
      data: { message: "echo: ready for review", model: "predictable" },
    });
    expect(reply.ok()).toBeTruthy();
    await expect(bold).toBeVisible();
    await expect(page.locator(`.conversation-item-unread${row}`)).toBeVisible();

    // Keep the receipt in flight until we've inspected both drawers.
    let release!: () => void;
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    const readPath = `/api/conversation/${conversationId}/read`;
    await page.route(
      `**${readPath}`,
      async (route) => {
        await held;
        await route.continue();
      },
      { times: 1 },
    );
    try {
      await page.bringToFront();
      await Promise.all([
        page.waitForRequest((r) => new URL(r.url()).pathname === readPath),
        page.locator(row).click(),
      ]);
      await expect(page.locator(row)).toHaveAttribute("aria-current", "page");
      await expect(page.locator(`.conversation-item-unread${row}`)).toHaveCount(0);
      await expect(bold).toBeVisible();
    } finally {
      release();
    }
    await expect(bold).toHaveCount(0);
    const other = await createConversationViaAPIWithDetails(request, "echo: another unread chat");
    await observer.reload();
    await expect(
      observer.locator(`.conversation-item-unread[data-conversation-id="${other.conversationId}"]`),
    ).toBeVisible();
    await expect(observer.locator(row)).toBeVisible();
    await expect(bold).toHaveCount(0);
  } finally {
    await observer.close();
  }
});

test.describe("with a mouse", () => {
  test.use({ hasTouch: false, isMobile: false, viewport: { width: 1280, height: 800 } });

  test("a working task's dot sits in the time's place; Archive pushes it left", async ({
    page,
    request,
  }) => {
    const idle = await createConversationViaAPIWithDetails(request, "echo: idle");
    const started = await request.post("/api/conversations/new", {
      data: { message: "bash: sleep 60", model: "predictable", cwd: testWorkingDirectory() },
    });
    expect(started.ok()).toBeTruthy();
    const working = { conversationId: (await started.json()).conversation_id as string };
    try {
      const row = (id: string) => page.locator(`.conversation-item[data-conversation-id="${id}"]`);
      await page.goto("/");
      await openWorkspace(page);
      const time = row(idle.conversationId).locator(".conversation-time");
      const dot = row(working.conversationId).getByTestId("drawer-working-indicator");
      await expect(time).toBeVisible();
      await expect(dot).toBeVisible();
      await expect(row(working.conversationId).locator(".conversation-time")).toHaveCount(0);
      // Right-aligned where the times are.
      const timeBox = (await time.boundingBox())!;
      const dotBox = (await dot.boundingBox())!;
      expect(Math.abs(dotBox.x + dotBox.width - (timeBox.x + timeBox.width))).toBeLessThan(2);

      await row(working.conversationId).hover();
      const archive = row(working.conversationId).getByRole("button", {
        name: "Archive",
        exact: true,
      });
      await expect(archive).not.toHaveCSS("opacity", "0");
      // Still shown, left of Archive.
      await expect(dot).toHaveCSS("opacity", "1");
      await expect
        .poll(async () => {
          const d = (await dot.boundingBox())!;
          const a = (await archive.boundingBox())!;
          return d.x + d.width <= a.x;
        })
        .toBe(true);
    } finally {
      await request.post(`/api/conversation/${working.conversationId}/cancel`, {
        headers: { "X-Shelley-Request": "1" },
      });
    }
  });

  test("a task row's Archive takes its time's place on hover or keyboard focus; no menu", async ({
    page,
    request,
  }) => {
    const { conversationId } = await createConversationViaAPIWithDetails(request, "Hello");
    const row = page.locator(`.conversation-item[data-conversation-id="${conversationId}"]`);
    await page.goto("/");
    await openWorkspace(page);
    const time = row.locator(".conversation-time");
    const archive = row.getByRole("button", { name: "Archive", exact: true });
    await expect(time).toHaveCSS("opacity", "1");
    await expect(archive).toHaveCSS("opacity", "0");
    const timeBox = (await time.boundingBox())!;
    await row.hover();
    await expect(time).toHaveCSS("opacity", "0");
    await expect(archive).not.toHaveCSS("opacity", "0");
    // Archive sits where the time was.
    const archiveBox = (await archive.boundingBox())!;
    expect(archiveBox.x).toBeLessThan(timeBox.x + timeBox.width);
    expect(archiveBox.x + archiveBox.width).toBeGreaterThan(timeBox.x);
    // No ⋯, and a right-click leaves the browser's own menu.
    await expect(row.getByRole("button", { name: "Actions" })).toHaveCount(0);
    await row.click({ button: "right" });
    await expect(page.getByTestId("conversation-context-menu")).toBeHidden();
    await page.mouse.move(0, 0);
    await expect(archive).toHaveCSS("opacity", "0");
    await expect(time).toHaveCSS("opacity", "1");
    // From the row, Tab reaches it; back on the row, it stays.
    await row.focus();
    await page.keyboard.press("Tab");
    await expect(archive).toBeFocused();
    await page.keyboard.press("Shift+Tab");
    await expect(row).toBeFocused();
    await expect(archive).not.toHaveCSS("opacity", "0");
    await archive.click();
    await expect(row).toHaveCount(0);
    const archived = await request.get(`/api/conversation/${conversationId}`);
    expect(
      ((await archived.json()) as { conversation: { archived: boolean } }).conversation.archived,
    ).toBe(true);
  });
});
