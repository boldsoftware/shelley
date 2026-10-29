import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { createConversationViaAPIWithDetails, testWorkingDirectory } from "./helpers";

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

  // The choice is remembered.
  await page.reload();
  await page.getByRole("button", { name: "Open conversations", exact: true }).click();
  await expect(group.locator(`[data-conversation-id="${projectConversationId}"]`)).toBeVisible();
});
