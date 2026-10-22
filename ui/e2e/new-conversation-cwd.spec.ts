import { expect, test, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import {
  createConversationViaAPIWithDetails,
  directoryControl,
  git,
  initGitRepo,
  withTempDir,
} from "./helpers";

async function startNewConversation(page: Page) {
  const header = page.locator(".header");
  await expect(header).toBeVisible();
  const button = header.getByRole("button", { name: "New Conversation", exact: true });
  if (await button.isVisible()) {
    await button.click();
    return;
  }
  await header.getByRole("button", { name: "More options", exact: true }).click();
  await page
    .locator(".chat-overflow-popover")
    .getByRole("button", { name: "New Conversation", exact: true })
    .click();
}

async function sendAndExpectCwd(page: Page, cwd: string) {
  const control = await directoryControl(page);
  await expect(control).toContainText(cwd);
  await page.keyboard.press("Escape");
  await sendAndExpectSavedCwd(page, "echo: new conversation directory", cwd);
}

async function sendAndExpectSavedCwd(page: Page, prompt: string, cwd: string) {
  await page.getByTestId("message-input").fill(prompt);
  const sent = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      /\/api\/(?:conversations\/new|conversation\/[^/]+\/chat)$/.test(response.url()),
  );
  await page.getByTestId("send-button").click();
  const response = await sent;
  expect(response.ok()).toBeTruthy();
  const id =
    response.url().match(/\/conversation\/([^/]+)\/chat$/)?.[1] ||
    (await response.json()).conversation_id;
  const body = await (await page.request.get(`/api/conversation/${id}`)).json();
  expect(body.conversation.cwd).toBe(cwd);
  expect(body.conversation.is_draft).toBeFalsy();
}

for (const location of [
  "worktree",
  "worktree subdirectory",
  "archived worktree",
  "ordinary directory",
]) {
  test(`new conversation from ${location} chooses the right directory`, async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-new-cwd-", async (dir) => {
      const repo = join(dir, "repo");
      const worktree = join(dir, "worktree");
      mkdirSync(repo);
      initGitRepo(repo);
      git(repo, "commit", "--allow-empty", "-m", "Initial commit");
      git(repo, "worktree", "add", "-b", "feature", worktree);
      const subdir = join(worktree, "subdir");
      mkdirSync(subdir);
      const cwd =
        location === "worktree subdirectory"
          ? subdir
          : location === "ordinary directory"
            ? dir
            : worktree;
      const expectedCwd = location === "ordinary directory" ? dir : repo;
      const { conversationId } = await createConversationViaAPIWithDetails(request, "Hello", {
        cwd,
      });
      if (location === "archived worktree") {
        const archived = await request.post(`/api/conversation/${conversationId}/archive`);
        expect(archived.ok()).toBeTruthy();
      }
      await page.goto(`/c/${conversationId}`);
      await startNewConversation(page);
      await expect(page).toHaveURL(/\/new$/);
      await sendAndExpectCwd(page, expectedCwd);
    });
  });
}

test("an explicitly selected worktree stays selected", async ({ page }) => {
  await withTempDir("shelley-new-cwd-", async (dir) => {
    const repo = join(dir, "repo");
    mkdirSync(repo);
    initGitRepo(repo);
    git(repo, "commit", "--allow-empty", "-m", "Initial commit");
    const worktree = join(dir, "worktree");
    git(repo, "worktree", "add", "-b", "feature", worktree);
    await page.addInitScript((cwd) => localStorage.setItem("shelley_selected_cwd", cwd), repo);
    await page.goto("/new");
    await (await directoryControl(page)).click();
    const dialog = page.getByRole("dialog", { name: "Select Directory" });
    await dialog.locator(".directory-picker-input").fill(worktree + "/");
    await expect(dialog.locator(".directory-picker-current-path")).toHaveText(worktree);
    await dialog.getByRole("button", { name: "Select", exact: true }).click();
    await sendAndExpectCwd(page, worktree);
  });
});

test("/new with a prompt uses the main repository", async ({ page, request }) => {
  await withTempDir("shelley-new-cwd-", async (dir) => {
    const repo = join(dir, "repo");
    mkdirSync(repo);
    initGitRepo(repo);
    git(repo, "commit", "--allow-empty", "-m", "Initial commit");
    const worktree = join(dir, "worktree");
    git(repo, "worktree", "add", "-b", "feature", worktree);
    const { conversationId } = await createConversationViaAPIWithDetails(request, "Hello", {
      cwd: worktree,
    });
    await page.goto(`/c/${conversationId}`);
    await sendAndExpectSavedCwd(page, "/new echo: new conversation directory", repo);
  });
});

test("directory lookup failures are visible and can be retried", async ({ page, request }) => {
  await withTempDir("shelley-new-cwd-", async (cwd) => {
    const { conversationId } = await createConversationViaAPIWithDetails(request, "Hello", { cwd });
    const archived = await request.post(`/api/conversation/${conversationId}/archive`);
    expect(archived.ok()).toBeTruthy();
    await page.goto(`/c/${conversationId}`);
    await page.route("**/api/list-directory?*", (route) =>
      route.fulfill({ status: 503, body: "Directory lookup unavailable" }),
    );
    await startNewConversation(page);
    await expect(page.getByRole("alert")).toContainText("Directory lookup unavailable");
    await expect(page).not.toHaveURL(/\/new$/);

    await page.unroute("**/api/list-directory?*");
    await page.getByRole("alert").getByRole("button", { name: "Dismiss" }).click();
    await startNewConversation(page);
    await sendAndExpectCwd(page, cwd);
  });
});
