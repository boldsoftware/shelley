import { test, expect, type Page } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { createConversationViaAPIWithDetails, testWorkingDirectory } from "./helpers";

const headers = { "X-Shelley-Request": "1" };

// Opens the drawer grouped by project, where projects are managed.
async function openProjects(page: Page) {
  await page.getByRole("button", { name: "Open conversations", exact: true }).click();
  await page.getByRole("button", { name: "Group conversations" }).click();
  await page
    .locator(".group-by-menu-item")
    .filter({ hasText: /^Project$/ })
    .click();
  await expect(page.getByRole("button", { name: "New project", exact: true })).toBeVisible();
}

test("create, edit and delete a project; start a conversation in it", async ({ page, request }) => {
  const plain = await createConversationViaAPIWithDetails(request, "Hello");
  await page.goto(`/c/${plain.conversationId}`);
  // Without projects, a conversation shows no project picker. Other specs on
  // this worker's server may have made some.
  await expect(page.getByTestId("message-input")).toBeEnabled();
  const before = (await (await request.get("/api/workspace")).json()) as { projects: unknown[] };
  if (!before.projects.length) {
    await expect(page.getByLabel("Conversation project", { exact: true })).toHaveCount(0);
  }
  await openProjects(page);
  await page.getByRole("button", { name: "New project", exact: true }).click();
  const newProject = page.getByRole("dialog", { name: "New project", exact: true });
  const name = `Publish ${randomUUID().slice(0, 8)}`;
  await newProject.getByLabel("Name", { exact: true }).fill(name);
  await newProject
    .getByLabel("Project instructions (AGENTS.md)", { exact: true })
    .fill("Keep release notes brief.");
  await newProject.getByLabel("Working directory", { exact: true }).fill(testWorkingDirectory());
  await newProject.getByRole("button", { name: "Save project", exact: true }).click();
  await expect(newProject).toBeHidden();

  await page.getByRole("button", { name: `${name} project actions`, exact: true }).click();
  const projectMenu = page.getByTestId("project-actions-menu");
  await expect(projectMenu.getByRole("menuitem")).toHaveText(["Edit project…", "Delete project…"]);
  await projectMenu.getByRole("menuitem", { name: "Edit project…", exact: true }).click();
  const editor = page.getByRole("dialog", { name: "Edit project", exact: true });
  await expect(editor.getByLabel("Project instructions (AGENTS.md)", { exact: true })).toHaveValue(
    "Keep release notes brief.",
  );
  await editor
    .getByLabel("Project instructions (AGENTS.md)", { exact: true })
    .fill("Keep release notes brief. Link the release.");
  await editor.getByRole("button", { name: "Save project", exact: true }).click();
  await expect(editor).toBeHidden();

  await page.getByRole("button", { name: `New conversation in ${name}`, exact: true }).click();
  const creator = page.getByRole("dialog", { name: "New Conversation", exact: true });
  await expect(creator.getByLabel("Project", { exact: true }).locator("option:checked")).toHaveText(
    name,
  );
  await creator.getByLabel("Title", { exact: false }).fill("Publish release notes");
  await creator.getByLabel("Instructions", { exact: true }).fill("Hello");
  await creator.getByRole("button", { name: "Start conversation", exact: true }).click();
  await expect(creator).toBeHidden();
  await expect(page).toHaveURL(/\/c\/publish-release-notes/);
  await expect(
    page.getByLabel("Conversation project", { exact: true }).locator("option:checked"),
  ).toHaveText(name);
  await page.getByRole("button", { name: "Edit conversation project", exact: true }).click();
  const edit = page.getByRole("dialog", { name: "Edit project", exact: true });
  await expect(edit.getByLabel("Project instructions (AGENTS.md)", { exact: true })).toHaveValue(
    "Keep release notes brief. Link the release.",
  );
  await edit.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(edit).toBeHidden();

  // Now that a project exists, a conversation outside it offers the picker.
  await page.goto(`/c/${plain.conversationId}`);
  await expect(
    page.getByLabel("Conversation project", { exact: true }).locator("option:checked"),
  ).toHaveText("No project");

  // Archiving its conversation lets the project be deleted; the archived
  // conversation moves to No project.
  const workspace = await request.get("/api/workspace");
  const { conversations } = (await workspace.json()) as {
    conversations: { conversation_id: string; project: string }[];
  };
  const created = conversations.find((c) => c.project === name);
  expect(created).toBeTruthy();
  const archived = await request.post(`/api/conversation/${created!.conversation_id}/archive`, {
    headers,
  });
  expect(archived.ok()).toBeTruthy();
  await openProjects(page);
  const group = page.getByRole("region", { name, exact: true });
  await expect(group).toBeVisible();
  await group.getByRole("button", { name: `${name} project actions`, exact: true }).click();
  await projectMenu.getByRole("menuitem", { name: "Delete project…", exact: true }).click();
  const remove = page.getByRole("dialog", { name: "Delete project?", exact: true });
  await expect(remove.getByTestId("project-delete-effects")).toHaveText(
    "Its archived conversation moves to No project.",
  );
  await remove.getByRole("button", { name: "Delete project", exact: true }).click();
  await expect(remove).toBeHidden();
  await expect(group).toHaveCount(0);
  const after = (await (await request.get("/api/workspace")).json()) as {
    projects: { name: string }[];
  };
  expect(after.projects.map((p) => p.name)).not.toContain(name);
});

test("project edits from elsewhere arrive through SSE", async ({ page, request }) => {
  const plain = await createConversationViaAPIWithDetails(request, "Hello");
  await page.goto(`/c/${plain.conversationId}`);
  await openProjects(page);
  const name = `Agent ${randomUUID().slice(0, 8)}`;
  const response = await request.post("/api/projects", {
    headers,
    data: { name, instructions: "Created outside this tab", cwd: testWorkingDirectory() },
  });
  expect(response.ok()).toBeTruthy();
  const project = await response.json();
  await expect(
    page.getByRole("button", { name: `New conversation in ${name}`, exact: true }),
  ).toBeVisible();
  const renamed = `${name} renamed`;
  const update = await request.put(`/api/projects/${encodeURIComponent(name)}`, {
    headers,
    data: { ...project, name: renamed },
  });
  expect(update.ok()).toBeTruthy();
  await expect(
    page.getByRole("button", { name: `New conversation in ${renamed}`, exact: true }),
  ).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`/c/(${plain.slug}|${plain.conversationId})$`));
});

test("project and conversation ⋯ menus: delete waits for an empty project; move to project", async ({
  page,
  request,
}) => {
  const project = async (name: string) => {
    const response = await request.post("/api/projects", {
      headers,
      data: { name, instructions: "", cwd: testWorkingDirectory() },
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

  await page.goto(`/c/${projectConversationId}`);
  await openProjects(page);
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

  // A project with active conversations can't be deleted, and its menu says why.
  await fromGroup.getByRole("button", { name: `${from} project actions`, exact: true }).click();
  const projectMenu = page.getByTestId("project-actions-menu");
  const remove = projectMenu.getByRole("menuitem", { name: "Delete project…" });
  await expect(remove).toHaveAttribute("aria-disabled", "true");
  await expect(remove.locator("[data-note]")).toHaveAttribute(
    "data-note",
    "Archive or move its active conversation first",
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
