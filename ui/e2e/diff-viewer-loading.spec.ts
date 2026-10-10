import { test, expect } from "@playwright/test";
import { writeFileSync } from "node:fs";
import {
  createConversationViaAPI,
  git,
  initGitRepo,
  openWorkspaceTool,
  withTempDir,
} from "./helpers";

test.describe("Diff viewer loading", () => {
  test("shows the commit message before changed files load and keeps it selected", async ({
    page,
    request,
  }) => {
    await page.setViewportSize({ width: 1280, height: 900 });

    await withTempDir("shelley-diff-loading-", async (cwd) => {
      initGitRepo(cwd);
      writeFileSync(`${cwd}/story.txt`, "before\n");
      git(cwd, "add", "story.txt");
      git(cwd, "commit", "-m", "initial story");
      writeFileSync(`${cwd}/story.txt`, "actual file content\n");
      git(cwd, "add", "story.txt");
      git(cwd, "commit", "-m", "update story");

      let releaseFiles!: () => void;
      const filesReleased = new Promise<void>((resolve) => {
        releaseFiles = resolve;
      });
      let filesRequestSeen!: () => void;
      const filesRequest = new Promise<void>((resolve) => {
        filesRequestSeen = resolve;
      });
      await page.route("**/api/git/diffs/*/files?*", async (route) => {
        filesRequestSeen();
        await filesReleased;
        await route.continue();
      });

      const slug = await createConversationViaAPI(request, "Hello", { cwd });
      await page.goto(`/c/${slug}`);
      await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
      await openWorkspaceTool(page, "Diffs");

      const overlay = page.locator(".diff-viewer-overlay");
      const fileSelect = overlay.locator("select.diff-viewer-select").first();
      await expect(overlay).toBeVisible();
      await filesRequest;
      await expect(fileSelect).toHaveValue(/^commit-message:/);
      await expect(overlay.locator(".diff-viewer-editor .editor.modified .view-lines")).toContainText(
        "update story",
      );
      await expect(fileSelect.locator('option[value="story.txt"]')).toHaveCount(0);

      releaseFiles();

      await expect(fileSelect.locator('option[value="story.txt"]')).toHaveCount(1);
      await expect(fileSelect).toHaveValue(/^commit-message:/);
    });
  });
});
