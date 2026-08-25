import { test, expect } from "@playwright/test";
import { writeFileSync, mkdirSync } from "node:fs";
import { join } from "node:path";
import { createConversationViaAPI, withTempDir } from "./helpers";

// The fuzzy file finder (Cmd/Ctrl+Shift+P) ANDs whitespace-separated terms, so
// a half-remembered filename typed as words finds the file: "vm storage s3"
// must reach vm-storage-s3-backup-design.md even though the literal query
// (spaces and all) never appears in the path.

test.describe("File finder multi-term search", () => {
  test("space-separated terms are ANDed", async ({ page, request }) => {
    await withTempDir("shelley-finder-", async (dir) => {
      mkdirSync(join(dir, "docs"), { recursive: true });
      for (const name of [
        "vm-storage-s3-backup-design.md",
        "vm-storage-replication.md",
        "s3-uploads.md",
      ]) {
        writeFileSync(join(dir, "docs", name), "x\n");
      }

      const slug = await createConversationViaAPI(request, "Hello", { cwd: dir });
      await page.goto(`/c/${slug}`);
      await page.waitForLoadState("domcontentloaded");

      await page.keyboard.press("ControlOrMeta+Shift+P");
      const finderInput = page.locator(".grp-filter");
      await expect(finderInput).toBeVisible({ timeout: 10000 });
      await finderInput.fill("vm storage s3");

      const rows = page.locator(".grp-row");
      await expect(rows).toHaveCount(1, { timeout: 10000 });
      await expect(rows.first()).toContainText("docs/vm-storage-s3-backup-design.md");
      // Each term underlines its literal run rather than a scattered subsequence.
      await expect(rows.first().locator("mark")).toHaveText(["vm", "storage", "s3"]);

      // A single term still matches every file containing it.
      await finderInput.fill("vm-storage");
      await expect(rows).toHaveCount(2, { timeout: 10000 });
    });
  });
});

// Typing a path re-roots the finder at that directory, so a file outside the
// conversation's working directory can still be opened in the editor.
test.describe("File finder path queries", () => {
  test("an absolute path searches that directory and opens the file", async ({ page, request }) => {
    test.setTimeout(60000);

    await withTempDir("shelley-finder-cwd-", async (cwd) => {
      writeFileSync(join(cwd, "local.md"), "local\n");

      await withTempDir("shelley-finder-else-", async (elsewhere) => {
        writeFileSync(join(elsewhere, "handoff-notes.md"), "far away content\n");

        const slug = await createConversationViaAPI(request, "Hello", { cwd });
        await page.goto(`/c/${slug}`);
        await page.waitForLoadState("domcontentloaded");

        await page.keyboard.press("ControlOrMeta+Shift+P");
        const finderInput = page.locator(".grp-filter");
        await expect(finderInput).toBeVisible({ timeout: 10000 });

        await finderInput.fill(join(elsewhere, "handoff"));

        const rows = page.locator(".grp-row");
        await expect(rows).toHaveCount(1, { timeout: 10000 });
        await expect(rows.first()).toContainText("handoff-notes.md");
        // The list is no longer relative to the working directory, so the
        // finder says where it is actually looking.
        await expect(page.locator(".ff-scope")).toContainText(elsewhere);

        // Enter opens the file from the re-rooted directory, not a path
        // joined against the conversation's cwd.
        await finderInput.press("Enter");
        const modal = page.getByRole("dialog", {
          name: `Edit ${join(elsewhere, "handoff-notes.md")}`,
        });
        await expect(modal).toBeVisible({ timeout: 15000 });
        await expect(
          modal.locator(".view-line", { hasText: "far away content" }).first(),
        ).toBeVisible({ timeout: 15000 });
      });
    });
  });

  test("clearing a path query returns to the working directory", async ({ page, request }) => {
    test.setTimeout(60000);

    await withTempDir("shelley-finder-back-", async (cwd) => {
      writeFileSync(join(cwd, "local-only.md"), "local\n");
      // A sibling to wander into. Not shared /tmp: that's every other test's
      // temp dir, and walking it is both slow and nondeterministic.
      const away = join(cwd, "away");
      mkdirSync(away, { recursive: true });
      writeFileSync(join(away, "remote.md"), "remote\n");

      const slug = await createConversationViaAPI(request, "Hello", { cwd });
      await page.goto(`/c/${slug}`);
      await page.waitForLoadState("domcontentloaded");

      await page.keyboard.press("ControlOrMeta+Shift+P");
      const finderInput = page.locator(".grp-filter");
      await expect(finderInput).toBeVisible({ timeout: 10000 });

      await finderInput.fill(away + "/");
      await expect(page.locator(".ff-scope")).toContainText(away, { timeout: 10000 });

      await finderInput.fill("local-only");
      const rows = page.locator(".grp-row");
      await expect(rows).toHaveCount(1, { timeout: 10000 });
      await expect(rows.first()).toContainText("local-only.md");
      await expect(page.locator(".ff-scope")).toHaveCount(0);
    });
  });

  test("a path to an empty directory says the directory is empty", async ({ page, request }) => {
    test.setTimeout(60000);

    await withTempDir("shelley-finder-empty-", async (cwd) => {
      writeFileSync(join(cwd, "local.md"), "local\n");
      const empty = join(cwd, "empty-dir");
      mkdirSync(empty, { recursive: true });

      const slug = await createConversationViaAPI(request, "Hello", { cwd });
      await page.goto(`/c/${slug}`);
      await page.waitForLoadState("domcontentloaded");

      await page.keyboard.press("ControlOrMeta+Shift+P");
      const finderInput = page.locator(".grp-filter");
      await expect(finderInput).toBeVisible({ timeout: 10000 });

      // Naming a directory lists all of it, so an empty one found nothing to
      // list rather than failing to match a pattern.
      await finderInput.fill(empty + "/");
      await expect(page.locator(".grp-empty")).toHaveText("No files in this directory.", {
        timeout: 10000,
      });

      // A pattern that matches nothing inside it reads differently.
      await finderInput.fill(join(empty, "zzz"));
      await expect(page.locator(".grp-empty")).toHaveText("No matching files.", {
        timeout: 10000,
      });
    });
  });
});
