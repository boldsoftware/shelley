import { expect, test, type Locator, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdirSync, renameSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createConversationViaAPI, withTempDir } from "./helpers";

const shelleyBin = resolve(fileURLToPath(new URL("../../bin/shelley", import.meta.url)));

function git(cwd: string, ...args: string[]): string {
  return execFileSync("git", args, { cwd, encoding: "utf8" }).trim();
}

async function openTour(page: Page, slug: string) {
  await page.setViewportSize({ width: 1800, height: 900 });
  // The table of contents only exists in the sidebar layout.
  await page.addInitScript(() => localStorage.setItem("diff-viewer-layout", "sidebar"));
  await page.goto(`/c/${slug}`);
  await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
  await page.locator(".chat-overflow-menu-wrapper .btn-icon").click();
  await page.locator(".overflow-menu-item", { hasText: /diffs/i }).click();
  const overlay = page.locator(".diff-viewer-overlay");
  await expect(overlay).toBeVisible({ timeout: 30000 });
  await expect(overlay.locator(".diff-viewer-view-switcher button.active")).toHaveText("Tour");
  return overlay;
}

// A stale table-of-contents jump would be re-applied by the tour view's resize
// handler a frame after the diff re-renders, so let two frames pass before
// judging where a change row ended up.
async function settleFrames(page: Page) {
  await page.evaluate(
    () =>
      new Promise<void>((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
      ),
  );
}

// Rows of the additions column inside the chunk's shadow-DOM diff.
function additionRows(chunk: Locator) {
  return chunk.locator("diffs-container").evaluate((element) => {
    const root = element.shadowRoot;
    if (!root) return null;
    return {
      lines: root.querySelectorAll("[data-additions] [data-line]").length,
      changes: root.querySelectorAll('[data-additions] [data-line][data-line-type^="change-"]')
        .length,
      separators: root.querySelectorAll("[data-separator]").length,
    };
  });
}

function changeRowTop(chunk: Locator, line: number) {
  return chunk.locator("diffs-container").evaluate((element, line) => {
    const row = element.shadowRoot?.querySelector(
      `[data-additions] [data-line="${line}"][data-line-type="change-addition"]`,
    );
    return row ? row.getBoundingClientRect().top : null;
  }, line);
}

test.describe("Commit tour full file", () => {
  test("a chunk expands to the whole file and back without losing its place", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-tour-full-file-", async (tempDir) => {
      const repo = join(tempDir, "repo");
      mkdirSync(join(repo, "src"), { recursive: true });
      git(repo, "init");
      git(repo, "config", "user.name", "Tour Test");
      git(repo, "config", "user.email", "tour@example.com");

      // Three changes far apart in one file: the tour gets one chunk per hunk,
      // and the whole file shows them all. Plus a renamed-and-edited file,
      // whose left side only resolves through the pre-rename path. Line text
      // deliberately differs from the "line N" wording of the comment dialog's
      // title so quoting the wrong line cannot pass unnoticed.
      const lines = Array.from(
        { length: 220 },
        (_, index) => `export const value${index + 1} = ${index + 1};`,
      );
      const examplePath = join(repo, "src", "example.ts");
      writeFileSync(examplePath, lines.join("\n") + "\n");
      const renamedLines = Array.from({ length: 40 }, (_, index) => `// comment ${index + 1}`);
      writeFileSync(join(repo, "old_name.go"), renamedLines.join("\n") + "\n");
      git(repo, "add", "src/example.ts", "old_name.go");
      git(repo, "commit", "-m", "Base commit\n\nPrompt: tour full file base");

      lines[1] = "export const top = 'updated';";
      lines[99] = "export const middle = 'updated';";
      lines[198] = "export const bottom = 'updated';";
      writeFileSync(examplePath, lines.join("\n") + "\n");
      renamedLines[19] = "// comment twenty (edited)";
      renameSync(join(repo, "old_name.go"), join(repo, "new_name.go"));
      writeFileSync(join(repo, "new_name.go"), renamedLines.join("\n") + "\n");
      git(repo, "add", "src/example.ts", "old_name.go", "new_name.go");
      git(repo, "commit", "-m", "Tour the top commit\n\nPrompt: tour full file feature");

      const scaffold = JSON.parse(
        execFileSync(shelleyBin, ["tour", "scaffold", "-C", repo, "HEAD"], {
          encoding: "utf8",
        }),
      );
      expect(scaffold.chunks).toHaveLength(4);
      const tour = {
        ...scaffold,
        title: "Tour the top commit",
        chunks: scaffold.chunks.map((chunk: { ref: number }, index: number) => ({
          ...chunk,
          comment: `Guided change ${index + 1}.`,
        })),
      };
      const tourPath = join(tempDir, "tour.json");
      writeFileSync(tourPath, JSON.stringify(tour));
      execFileSync(shelleyBin, ["tour", "attach", "-C", repo, "HEAD", tourPath]);

      const slug = await createConversationViaAPI(request, "Hello", { cwd: repo });
      const overlay = await openTour(page, slug);
      const tourView = overlay.locator(".commit-tour-view");
      const chunks = overlay.locator(".commit-tour-chunk");
      await expect(chunks).toHaveCount(4);

      // Chunk order follows the scaffold: the renamed file sorts first.
      const renamedChunk = chunks.nth(0);
      const bottomChunk = chunks.nth(3);
      await expect(renamedChunk.locator(".commit-tour-chunk-header code")).toHaveText(
        /^new_name\.go · \d+–\d+$/,
      );
      await expect(bottomChunk.locator(".commit-tour-chunk-header code")).toHaveText(
        /^src\/example\.ts · 196–202$/,
      );

      // Arrive the way a reader does, from the table of contents. That jump
      // stays pinned across ordinary layout shifts, and must not be allowed
      // to drag the header back to the top after the mode switch below.
      const contents = overlay.getByRole("navigation", { name: "Tour contents" });
      const bottomLink = contents.locator(".tour-change-link").nth(3);
      await bottomLink.click();
      await expect(bottomLink).toHaveAttribute("aria-current", "location");
      await expect(bottomChunk).toBeInViewport();

      // The patch-only render: a handful of context lines behind a separator.
      await expect
        .poll(() => additionRows(bottomChunk))
        .toEqual({
          lines: 7,
          changes: 1,
          separators: 4,
        });
      const toggle = bottomChunk.locator(".commit-tour-full-file-btn");
      await expect(toggle).toHaveAccessibleName("Show full src/example.ts");
      const beforeTop = await changeRowTop(bottomChunk, 199);
      expect(beforeTop).not.toBeNull();
      const scrollBefore = await tourView.evaluate((element) => element.scrollTop);

      // Clicking the "N unmodified lines" separator loads the whole file, and
      // the whole file includes the commit's other hunks in this file too.
      await bottomChunk.locator("diffs-container").evaluate((element) => {
        const content = element.shadowRoot?.querySelector<HTMLElement>("[data-separator-content]");
        if (!content) throw new Error("no separator");
        content.click();
      });
      await expect(toggle).toHaveAccessibleName("Show changes only in src/example.ts");
      await expect
        .poll(() => additionRows(bottomChunk))
        .toEqual({
          lines: 220,
          changes: 3,
          separators: 0,
        });
      // The 195 lines inserted above the change scrolled the view by the same
      // amount, so the change did not move on screen.
      await settleFrames(page);
      expect(await changeRowTop(bottomChunk, 199)).toBeCloseTo(beforeTop!, 0);
      expect(await tourView.evaluate((element) => element.scrollTop)).toBeGreaterThan(
        scrollBefore + 3000,
      );

      // Commenting on a line that only exists in the full file quotes it.
      await bottomChunk.locator("diffs-container").evaluate((element) => {
        const number = element.shadowRoot?.querySelector<HTMLElement>(
          '[data-additions] [data-column-number="180"]',
        );
        if (!number) throw new Error("no line 180");
        number.scrollIntoView({ block: "center" });
        number.click();
      });
      const commentDialog = overlay.locator(".diff-viewer-comment-dialog");
      await expect(commentDialog).toBeVisible();
      await expect(commentDialog).toContainText("src/example.ts line 180 (new)");
      await expect(commentDialog).toContainText("export const value180 = 180;");
      await commentDialog.getByRole("button", { name: "Cancel" }).click();
      await expect(commentDialog).toHaveCount(0);

      // Toggling back restores the patch view.
      await toggle.click();
      await expect(toggle).toHaveAccessibleName("Show full src/example.ts");
      await expect
        .poll(() => additionRows(bottomChunk))
        .toEqual({
          lines: 7,
          changes: 1,
          separators: 4,
        });

      // The renamed file's left side comes from its pre-rename path, so the
      // whole file is a one-line edit rather than 40 additions. With content
      // below this chunk the view can scroll either way, so both directions
      // keep the change where it was; dispatchEvent rather than click so
      // Playwright does not scroll the header into view first.
      await renamedChunk.evaluate((element) => element.scrollIntoView({ block: "start" }));
      const renamedToggle = renamedChunk.locator(".commit-tour-full-file-btn");
      await expect(renamedToggle).toHaveAccessibleName("Show full new_name.go");
      const renamedBefore = await changeRowTop(renamedChunk, 20);
      expect(renamedBefore).not.toBeNull();
      await renamedToggle.dispatchEvent("click");
      await expect
        .poll(() => additionRows(renamedChunk))
        .toEqual({
          lines: 40,
          changes: 1,
          separators: 0,
        });
      await settleFrames(page);
      expect(await changeRowTop(renamedChunk, 20)).toBeCloseTo(renamedBefore!, 0);
      await renamedToggle.dispatchEvent("click");
      await expect
        .poll(() => additionRows(renamedChunk))
        .toEqual({
          lines: 7,
          changes: 1,
          separators: 4,
        });
      await settleFrames(page);
      expect(await changeRowTop(renamedChunk, 20)).toBeCloseTo(renamedBefore!, 0);
    });
  });
});
