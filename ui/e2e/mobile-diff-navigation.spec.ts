import { expect, test } from "@playwright/test";
import { writeFileSync } from "node:fs";
import { join } from "node:path";
import {
  createConversationViaAPI,
  git,
  initGitRepo,
  openWorkspaceTool,
  withTempDir,
} from "./helpers";

test("mobile diff navigation advances through each hunk in order", async ({ page, request }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await withTempDir("shelley-mobile-hunks-", async (cwd) => {
    initGitRepo(cwd);
    const file = join(cwd, "hunks.txt");
    const lines = Array.from({ length: 50 }, (_, i) => `line ${i + 1}`);
    writeFileSync(file, `${lines.join("\n")}\n`);
    git(cwd, "add", ".");
    git(cwd, "commit", "-m", "base");
    for (const line of [5, 20, 35]) lines[line - 1] += " changed";
    writeFileSync(file, `${lines.join("\n")}\n`);
    const slug = await createConversationViaAPI(request, "Hello", { cwd });
    await page.goto(`/c/${slug}`);
    await openWorkspaceTool(page, "Diffs");
    const overlay = page.locator(".diff-viewer-overlay");
    await expect(overlay).toBeVisible();
    const fileRow = overlay.locator('.diff-rethink-mobile-files .diff-tree-code-row[title="hunks.txt"]');
    const added = await fileRow.locator(".diff-tree-changes-added").boundingBox();
    const deleted = await fileRow.locator(".diff-tree-changes-deleted").boundingBox();
    expect(added && deleted).toBeTruthy();
    expect(Math.abs(added!.y - deleted!.y)).toBeLessThan(2);
    await overlay
      .locator(".diff-rethink-mobile-files")
      .getByRole("treeitem", { name: "hunks.txt" })
      .click();
    await expect(overlay.locator(".diff-rethink-file-name")).toHaveText("hunks.txt");

    const label = overlay.locator(".diff-rethink-change-label");
    await expect(label).toHaveText("Change 1 of 3");
    await overlay.getByRole("button", { name: "Next change (.)" }).click();
    await expect(label).toHaveText("Change 2 of 3");
    await overlay.getByRole("button", { name: "Next change (.)" }).click();
    await expect(label).toHaveText("Change 3 of 3");
    await overlay.getByRole("button", { name: "Previous change (,)" }).click();
    await expect(label).toHaveText("Change 2 of 3");
  });
});
