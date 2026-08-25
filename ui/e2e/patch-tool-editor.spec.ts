import { test, expect } from "@playwright/test";
import { writeFileSync, readFileSync, mkdirSync } from "node:fs";
import { join } from "node:path";
import { createConversationViaAPI, withTempDir } from "./helpers";

// A patch tool card's header offers "open in editor": it opens the patched
// file in the same standalone Monaco editor the fuzzy finder uses, so a patch
// can be inspected/fixed in place without hunting for the path.

test.describe("Patch tool open-in-editor", () => {
  test("header button opens the patched file in the editor", async ({ page, request }) => {
    test.setTimeout(60000);

    await withTempDir("shelley-patchedit-", async (dir) => {
      const filePath = join(dir, "notes.txt");
      writeFileSync(filePath, "an example line\n");

      // The predictable model's "patch: <path>" replaces "example" in that file.
      const slug = await createConversationViaAPI(request, `patch: ${filePath}`, { cwd: dir });
      expect(readFileSync(filePath, "utf8")).toContain("updated example");

      await page.goto(`/c/${slug}`);
      await page.waitForLoadState("domcontentloaded");

      const patchTool = page.locator('.patch-tool[data-testid="tool-call-completed"]').first();
      await expect(patchTool).toBeVisible({ timeout: 15000 });

      await patchTool.getByRole("button", { name: "Open in editor" }).click();

      const modal = page.getByRole("dialog", { name: `Edit ${filePath}` });
      await expect(modal).toBeVisible({ timeout: 15000 });
      await expect(modal.locator(".view-line", { hasText: "updated example" }).first()).toBeVisible(
        {
          timeout: 15000,
        },
      );

      // Closing the editor leaves the patch card intact (it is not nested in it).
      await page.keyboard.press("Escape");
      await expect(modal).not.toBeVisible();
      await expect(patchTool).toBeVisible();
    });
  });

  test("a relative patch path resolves against the conversation cwd", async ({ page, request }) => {
    test.setTimeout(60000);

    // A successful patch records an absolutized path, so the relative branch is
    // only reachable for a FAILED patch, where the card falls back to the path
    // the agent passed. "example" is missing from this file, so the replace
    // fails and the card keeps "./sub/notes.txt".
    await withTempDir("shelley-patchedit-rel-", async (dir) => {
      mkdirSync(join(dir, "sub"), { recursive: true });
      writeFileSync(join(dir, "sub", "notes.txt"), "nothing to replace here\n");

      const slug = await createConversationViaAPI(request, "patch: ./sub/notes.txt", { cwd: dir });
      await page.goto(`/c/${slug}`);
      await page.waitForLoadState("domcontentloaded");

      const patchTool = page.locator('.patch-tool[data-testid="tool-call-completed"]').first();
      await expect(patchTool).toBeVisible({ timeout: 15000 });
      await expect(patchTool.locator(".patch-tool-filename")).toHaveText("./sub/notes.txt");
      await expect(patchTool.locator(".patch-tool-error")).toBeVisible();

      await patchTool.getByRole("button", { name: "Open in editor" }).click();

      // Resolved against the conversation cwd into an absolute path.
      const modal = page.getByRole("dialog", { name: `Edit ${join(dir, "sub", "notes.txt")}` });
      await expect(modal).toBeVisible({ timeout: 15000 });
      await expect(
        modal.locator(".view-line", { hasText: "nothing to replace here" }).first(),
      ).toBeVisible({ timeout: 15000 });
    });
  });
});
