import { expect, test, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
  createConversationViaAPI,
  git,
  initGitRepo,
  openWorkspaceTool,
  withTempDir,
} from "./helpers";

const shelleyBin = resolve(fileURLToPath(new URL("../../bin/shelley", import.meta.url)));

async function openDiffViewer(
  page: Page,
  slug: string,
  layout: "sidebar" | "header",
  viewport = { width: 1600, height: 900 },
) {
  await page.setViewportSize(viewport);
  await page.addInitScript((value) => localStorage.setItem("diff-viewer-layout", value), layout);
  await page.goto(`/c/${slug}`);
  await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
  await openWorkspaceTool(page, "Diffs");
  const overlay = page.locator(".diff-viewer-overlay");
  await expect(overlay).toBeVisible({ timeout: 30000 });
  return overlay;
}

// A repo whose top commit touches three files and carries highlights that
// rank them against alphabetical order.
function makeRepo(tempDir: string, extraHighlights: object[] = []): string {
  const repo = join(tempDir, "repo");
  mkdirSync(join(repo, "src"), { recursive: true });
  initGitRepo(repo);
  const lines = Array.from({ length: 200 }, (_, index) => `line ${index + 1}`);
  writeFileSync(join(repo, "src", "zeta.ts"), lines.join("\n") + "\n");
  writeFileSync(join(repo, "src", "alpha.ts"), "export const alpha = 1;\n");
  writeFileSync(join(repo, "README.md"), "# Demo\n");
  git(repo, "add", ".");
  git(repo, "commit", "-m", "Base commit");

  lines[159] = "const critical = true;";
  writeFileSync(join(repo, "src", "zeta.ts"), lines.join("\n") + "\n");
  writeFileSync(join(repo, "src", "alpha.ts"), "export const alpha = 2;\n");
  writeFileSync(join(repo, "README.md"), "# Demo\n\nNow with highlights.\n");
  git(repo, "add", ".");
  git(repo, "commit", "-m", "Highlighted commit");

  const highlights = {
    version: 1,
    files: ["src/zeta.ts", "README.md", "src/alpha.ts"],
    highlights: [
      { file: "src/zeta.ts", line: 160, comment: "The one line that matters." },
      { file: "README.md", line: 3, comment: "Docs follow the code." },
      ...extraHighlights,
    ],
  };
  const highlightsPath = join(tempDir, "highlights.json");
  writeFileSync(highlightsPath, JSON.stringify(highlights));
  execFileSync(shelleyBin, ["highlights", "attach", "-C", repo, "HEAD", highlightsPath]);
  return repo;
}

test.describe("Commit highlights", () => {
  test("the sidebar ranks files and marks highlighted lines", async ({ page, request }) => {
    await withTempDir("shelley-highlights-", async (tempDir) => {
      const repo = makeRepo(tempDir);
      const slug = await createConversationViaAPI(request, "Hello", { cwd: repo });
      const overlay = await openDiffViewer(page, slug, "sidebar");

      // A clean tree defaults to the highlighted top commit.
      await expect(overlay.getByRole("radio", { name: "Single commit" })).toHaveAttribute(
        "aria-checked",
        "true",
      );
      await expect(
        overlay.locator(".diff-viewer-commit-list-item .commit-picker-highlights-badge"),
      ).toHaveText("highlights");
      await expect(overlay.getByText("Commit Messages and Files by Importance")).toBeVisible();

      const files = overlay.locator(".diff-tree-row[aria-selected]");
      await expect(files).toHaveCount(4);
      await expect(files.locator(".diff-tree-label")).toHaveText([
        "Highlighted commit",
        "zeta.ts",
        "README.md",
        "alpha.ts",
      ]);
      await expect(files.first().locator(".ellipsis-start")).toHaveCount(0);
      await expect(files.nth(1).locator(".ellipsis-start")).toHaveCount(1);
      // Each file sits under its folder, so src repeats around README.md.
      const folders = overlay.locator(".diff-tree-row:not([aria-selected]) .diff-tree-label");
      await expect(overlay.locator(".diff-tree-row .diff-tree-label")).toHaveText([
        "Commit messages",
        "Highlighted commit",
        "src",
        "zeta.ts",
        "README.md",
        "src",
        "alpha.ts",
      ]);

      // Files show how many highlights they carry, not the comments.
      const counts = files.locator(".diff-tree-highlight-count");
      await expect(counts).toHaveText(["★1", "★1"]);
      await expect(files.nth(1)).toHaveAccessibleName(/1 highlight/);
      await expect(files.nth(3).locator(".diff-tree-highlight-count")).toHaveCount(0);
      await expect(overlay.getByText("The one line that matters.")).toHaveCount(0);

      // A file opens at its first highlight, with the comment under it.
      await files.nth(1).click();
      await expect(files.nth(1)).toHaveAttribute("aria-selected", "true");
      const callout = overlay.locator(".diff-highlight-callout-text");
      await expect(callout).toHaveText("The one line that matters.");
      await expect(callout).toBeInViewport();
      await expect(overlay.locator(".view-lines").last()).toContainText("const critical = true;");
      await expect(overlay.locator(".diff-highlight-gutter")).toHaveCount(1);

      // Next file follows the ranked order.
      await overlay.getByRole("button", { name: "Next file (>)" }).click();
      await expect(files.nth(2)).toHaveAttribute("aria-selected", "true");
      await expect(callout).toHaveText("Docs follow the code.");
      await overlay.getByRole("button", { name: "Next file (>)" }).click();
      await expect(files.nth(3)).toHaveAttribute("aria-selected", "true");
      await expect(callout).toHaveCount(0);
      await expect(overlay.locator(".diff-highlight-gutter")).toHaveCount(0);

      // Highlights describe one commit; a range keeps the alphabetical tree.
      await overlay.getByRole("radio", { name: "Through working tree" }).click();
      await expect(overlay.getByText("Commit Messages and Files", { exact: true })).toBeVisible();
      await expect(counts).toHaveCount(0);
      await expect(folders).toHaveText(["Commit messages", "src"]);
    });
  });

  test("a callout's Comment button answers the highlight", async ({ page, request }) => {
    await withTempDir("shelley-highlights-", async (tempDir) => {
      const repo = makeRepo(tempDir);
      const slug = await createConversationViaAPI(request, "Hello", { cwd: repo });
      const overlay = await openDiffViewer(page, slug, "sidebar");
      await overlay.locator(".diff-tree-row[aria-selected]").nth(1).click();
      await expect(overlay.locator(".diff-highlight-callout-text")).toBeInViewport();

      // Comment mode marks the line under the pointer, but not over a
      // callout, where a click doesn't comment on a line.
      const hover = overlay.locator(".diff-viewer-line-hover");
      await overlay.locator(".view-line", { hasText: "const critical = true;" }).hover();
      await expect(hover).toHaveCount(1);
      // The callout lets the pointer through to the space reserved for it.
      const box = (await overlay.locator(".diff-highlight-callout-text").boundingBox())!;
      await page.mouse.move(box.x + 10, box.y + box.height / 2);
      await expect(hover).toHaveCount(0);

      // Screen readers reach the callout and its button.
      const note = overlay.getByRole("note");
      await expect(note).toHaveText(/The one line that matters\./);
      const button = note.getByRole("button", { name: "Comment" });
      // A quiet icon with a full touch target that doesn't heighten the note.
      const target = (await button.boundingBox())!;
      expect([target.width, target.height]).toEqual([24, 24]);
      const text = (await note.locator(".diff-highlight-callout-text").boundingBox())!;
      expect((await note.boundingBox())!.height).toBeCloseTo(text.height + 8, 0);
      await button.click();
      const dialog = page.locator(".diff-viewer-comment-dialog");
      await expect(dialog).toContainText("Add Comment (Line 160, new)");
      await expect(dialog.locator(".diff-viewer-selected-text")).toHaveText(
        "const critical = true;\n\u2605 The one line that matters.",
      );
      await dialog.locator(".diff-viewer-comment-input").fill("Why this line?");
      await dialog.getByRole("button", { name: "Add Comment" }).click();
      await expect(dialog).not.toBeVisible();

      // The comment quotes the line and the highlight it answers.
      await page.keyboard.press("Escape");
      await expect(overlay).not.toBeVisible();
      await expect(page.getByTestId("message-input")).toHaveValue(
        "> src/zeta.ts:160: const critical = true;\n" +
          "> \u2605 The one line that matters.\n" +
          "Why this line?\n\n",
      );
    });
  });

  test("tabbing to a callout's button scrolls it into view", async ({ page, request }) => {
    await withTempDir("shelley-highlights-", async (tempDir) => {
      const repo = makeRepo(tempDir, [
        { file: "src/zeta.ts", line: 10, comment: "Far above the other." },
      ]);
      const slug = await createConversationViaAPI(request, "Hello", { cwd: repo });
      const overlay = await openDiffViewer(page, slug, "sidebar");
      await overlay.locator(".diff-tree-row[aria-selected]").nth(1).click();

      // The file opens at its first highlight; the second is far below.
      const notes = overlay.getByRole("note");
      await expect(notes.first()).toBeInViewport();
      await expect(notes.last()).not.toBeInViewport();

      await notes.first().getByRole("button", { name: "Comment" }).focus();
      await page.keyboard.press("Tab");
      const second = notes.last().getByRole("button", { name: "Comment" });
      await expect(second).toBeFocused();
      await expect(notes.last()).toBeInViewport();
      await expect(notes.last()).toHaveText(/The one line that matters\./);

      await page.keyboard.press("Enter");
      await expect(page.locator(".diff-viewer-comment-dialog")).toContainText(
        "Add Comment (Line 160, new)",
      );
    });
  });

  test("callouts keep their space as the editor narrows", async ({ page, request }) => {
    await withTempDir("shelley-highlights-", async (tempDir) => {
      const comment = "A long highlight that wraps onto more lines as the editor narrows. ".repeat(
        3,
      );
      const repo = makeRepo(tempDir, [{ file: "src/zeta.ts", line: 10, comment: comment.trim() }]);
      const slug = await createConversationViaAPI(request, "Hello", { cwd: repo });
      const overlay = await openDiffViewer(page, slug, "sidebar");
      await overlay.locator(".diff-tree-row[aria-selected]").nth(1).click();

      // The line after the highlight starts where its callout ends.
      const callout = overlay.locator(".diff-highlight-zone").first();
      const nextLine = overlay.locator(".editor.modified .view-line", { hasText: /^line\s11$/ });
      const gap = async () => {
        // Monaco re-renders lines as it lays out; retry until both show.
        const below = await nextLine.boundingBox();
        const box = await callout.boundingBox();
        return below && box ? below.y - (box.y + box.height) : NaN;
      };
      await expect(callout).toBeInViewport();
      await expect.poll(gap).toBeCloseTo(0, 0);
      const height = (await callout.boundingBox())!.height;

      await page.setViewportSize({ width: 800, height: 900 });
      await expect
        .poll(async () => (await callout.boundingBox())?.height ?? 0)
        .toBeGreaterThan(height);
      await expect.poll(gap).toBeCloseTo(0, 0);
    });
  });

  test("a phone's file picker follows the ranked order", async ({ page, request }) => {
    await withTempDir("shelley-highlights-", async (tempDir) => {
      const repo = makeRepo(tempDir);
      const slug = await createConversationViaAPI(request, "Hello", { cwd: repo });
      const overlay = await openDiffViewer(page, slug, "header", { width: 390, height: 844 });
      const select = overlay
        .locator(".diff-viewer-file-selector-wrapper select")
        .filter({ visible: true });
      const options = select.locator("option");
      // A placeholder, the commit message, then the ranked files.
      await expect(options).toHaveCount(5);
      await expect(options.nth(2)).toHaveText(/ src\/zeta\.ts .*★1$/);
      await expect(options.nth(3)).toHaveText(/ README\.md .*★1$/);
      await expect(options.nth(4)).toHaveText(/ src\/alpha\.ts /);

      // The narrow diff is inline; the comment still shows under its line.
      await select.selectOption("src/zeta.ts");
      const callout = overlay.locator(".diff-highlight-callout-text");
      await expect(callout).toHaveText("The one line that matters.");
      await expect(callout).toBeInViewport();

      // A tap on the callout's button opens the comment dialog, though
      // Monaco claims touches inside the editor.
      await overlay.getByRole("note").getByRole("button", { name: "Comment" }).tap();
      await expect(page.locator(".diff-viewer-comment-dialog")).toContainText(
        "Add Comment (Line 160, new)",
      );
    });
  });
});
