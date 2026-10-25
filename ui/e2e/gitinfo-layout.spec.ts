import { expect, test } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { createConversationViaAPIWithDetails, git, initGitRepo, withTempDir } from "./helpers";

test("git info keeps the approved compact layout in both themes", async ({ page, request }) => {
  await withTempDir("shelley-gitinfo-layout-", async (tempDir) => {
    const repo = join(tempDir, "projects", "long-parent-context", "worktrees", "active-checkout");
    const branch = "feature/keep-long-conversation-metadata-readable";
    const subject =
      "Keep full commit subjects and branch names readable in the compact conversation git strip";
    mkdirSync(repo, { recursive: true });
    initGitRepo(repo);
    writeFileSync(join(repo, "example.txt"), "before\n");
    git(repo, "add", ".");
    git(repo, "commit", "-m", "Base commit");
    git(repo, "switch", "-c", branch);
    const { slug } = await createConversationViaAPIWithDetails(
      request,
      `bash: printf 'after\\n' > example.txt && git add example.txt && git commit -m '${subject}'`,
      { cwd: repo },
    );
    const shortHash = git(repo, "rev-parse", "--short", "HEAD");
    await page.goto(`/c/${slug}`);
    const strip = page.getByTestId("message-gitinfo").filter({ hasText: shortHash });
    const diff = strip.getByRole("link", { name: "Open Diff", exact: true });
    await expect(diff).toBeVisible();
    await expect(strip.locator(".msg-subject")).toHaveText(subject);
    await expect(strip.locator(".msg-subject")).toHaveAttribute("title", subject);
    await expect(strip.locator(".msg-branch")).toHaveText(branch);
    await expect(strip.locator(".msg-worktree")).toHaveAttribute("title", repo);
    await expect(diff).toHaveAttribute("href", new RegExp(`cwd=${encodeURIComponent(repo)}`));

    for (const dark of [false, true]) {
      await page.evaluate(
        (enabled) => document.documentElement.classList.toggle("dark", enabled),
        dark,
      );
      await expect(strip).toHaveCSS(
        "background-color",
        dark ? "rgb(31, 41, 55)" : "rgb(243, 244, 246)",
      );
      for (const width of [1280, 960, 390]) {
        await page.setViewportSize({ width, height: 900 });
        await expect(async () => {
          const layout = await strip.evaluate((element) => {
            const rect = (selector: string) =>
              element.querySelector(selector)!.getBoundingClientRect().toJSON();
            return {
              title: rect(".msg-subject"),
              branch: rect(".msg-branch"),
              hash: rect(".msg-commit-hash"),
              path: rect(".msg-worktree"),
              diff: rect(".msg-diff-link"),
              fonts: [
                ...element.querySelectorAll(
                  ".msg-subject, .msg-branch, .msg-commit-hash, .msg-worktree",
                ),
              ].map((child) => getComputedStyle(child).fontSize),
              actionFont: getComputedStyle(element.querySelector(".msg-diff-link")!).fontSize,
              pathLabel: element.querySelector(".msg-worktree")!.textContent!.trim(),
              overflow: document.documentElement.scrollWidth > window.innerWidth,
              branchOverflow:
                element.querySelector(".msg-branch")!.scrollWidth >
                element.querySelector(".msg-branch")!.clientWidth + 1,
            };
          });
          expect(layout.fonts.every((font) => font === "14px")).toBe(true);
          expect(layout.actionFont).toBe("12px");
          expect(layout.overflow).toBe(false);
          expect(Math.abs(layout.title.x - layout.branch.x)).toBeLessThan(1);
          expect(layout.diff.height).toBe(22);
          expect(layout.pathLabel.endsWith("active-checkout")).toBe(true);
          if (width > 640) {
            expect(layout.title.height).toBe(20);
            expect(layout.diff.y - layout.title.bottom).toBe(7);
            expect(
              Math.abs(
                layout.branch.y + layout.branch.height / 2 - layout.diff.y - layout.diff.height / 2,
              ),
            ).toBeLessThan(1);
          } else {
            expect(layout.branchOverflow).toBe(false);
            expect(layout.diff.y).toBeGreaterThanOrEqual(
              Math.max(layout.branch.bottom, layout.hash.bottom, layout.path.bottom),
            );
          }
        }).toPass();
      }
    }
    await diff.click();
    await expect(page.locator(".diff-viewer-overlay")).toBeVisible();
  });
});

test("git info aligns detached HEAD metadata with the subject", async ({ page, request }) => {
  await withTempDir("shelley-gitinfo-detached-", async (repo) => {
    initGitRepo(repo);
    writeFileSync(join(repo, "example.txt"), "before\n");
    git(repo, "add", ".");
    git(repo, "commit", "-m", "Base commit");
    git(repo, "checkout", "--detach");
    const { slug } = await createConversationViaAPIWithDetails(
      request,
      "bash: printf 'after\\n' > example.txt && git add example.txt && git commit -m 'Detached HEAD change'",
      { cwd: repo },
    );
    await page.goto(`/c/${slug}`);
    const strip = page.getByTestId("message-gitinfo").filter({
      hasText: git(repo, "rev-parse", "--short", "HEAD"),
    });
    await expect(strip.locator(".msg-subject")).toBeVisible();
    await expect(strip.locator(".msg-branch")).toHaveCount(0);
    for (const width of [1280, 960, 390]) {
      await page.setViewportSize({ width, height: 900 });
      await expect(async () => {
        const offsets = await strip.evaluate((element) => ({
          title: element.querySelector(".msg-subject")!.getBoundingClientRect().x,
          hash: element.querySelector(".msg-commit-hash")!.getBoundingClientRect().x,
        }));
        expect(Math.abs(offsets.title - offsets.hash)).toBeLessThan(1);
      }).toPass();
    }
  });
});
