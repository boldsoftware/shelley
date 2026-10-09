import { expect, test, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
  openWorkspaceTool,
  createConversationViaAPI,
  git,
  initGitRepo,
  makePNG,
  withTempDir,
} from "./helpers";

const shelleyBin = resolve(fileURLToPath(new URL("../../bin/shelley", import.meta.url)));

async function openDiffViewer(page: Page, slug: string) {
  await page.setViewportSize({ width: 1800, height: 900 });
  await page.addInitScript(() => localStorage.setItem("diff-viewer-layout", "sidebar"));
  await page.goto(`/c/${slug}`);
  await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
  await openWorkspaceTool(page, "Diffs");
  const overlay = page.locator(".diff-viewer-overlay");
  await expect(overlay).toBeVisible({ timeout: 30000 });
  return overlay;
}

test.describe("Commit tour defaults", () => {
  test("a clean HEAD tour opens by default with tour contents in the sidebar", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-tour-viewer-", async (tempDir) => {
      const repo = join(tempDir, "repo");
      mkdirSync(repo);
      initGitRepo(repo);

      const lines = Array.from({ length: 220 }, (_, index) => `line ${index + 1}`);
      const examplePath = join(repo, "src", "nested", "example.spec.ts");
      mkdirSync(join(repo, "src", "nested"), { recursive: true });
      writeFileSync(examplePath, lines.join("\n") + "\n");
      git(repo, "add", "src/nested/example.spec.ts");
      git(repo, "commit", "-m", "Base commit");
      git(repo, "branch", "upstream");
      git(repo, "switch", "-c", "feature");
      git(repo, "branch", "--set-upstream-to=upstream", "feature");

      lines[1] = "updated near the top";
      lines[198] = "updated near the bottom";
      writeFileSync(examplePath, lines.join("\n") + "\n");
      git(repo, "add", "src/nested/example.spec.ts");
      git(repo, "commit", "-m", "Tour the top commit");

      const scaffold = JSON.parse(
        execFileSync(shelleyBin, ["tour", "scaffold", "-C", repo, "HEAD"], {
          encoding: "utf8",
        }),
      );
      expect(scaffold.chunks).toHaveLength(2);
      const tour = {
        ...scaffold,
        title: "Tour the top commit",
        intro: Array.from(
          { length: 24 },
          (_, index) => `Overview paragraph ${index + 1} explains the guided change.`,
        ).join("\n\n"),
        chunks: [
          { header: "## Core behavior" },
          ...Array.from({ length: 12 }, (_, index) => ({
            header: `### Detail ${index + 1}`,
          })),
          ...scaffold.chunks.map((chunk: { ref: number }, index: number) => ({
            ...chunk,
            comment: `Guided change ${index + 1}.`,
          })),
        ],
      };
      const tourPath = join(tempDir, "tour.json");
      writeFileSync(tourPath, JSON.stringify(tour));
      execFileSync(shelleyBin, ["tour", "attach", "-C", repo, "HEAD", tourPath]);

      const slug = await createConversationViaAPI(request, "Hello", { cwd: repo });
      const overlay = await openDiffViewer(page, slug);
      await expect(overlay.getByRole("radio", { name: "Single commit" })).toHaveAttribute(
        "aria-checked",
        "true",
      );
      await expect(overlay.locator(".diff-viewer-view-switcher button.active")).toHaveText("Tour");
      await expect(overlay.locator(".commit-tour-introduction h1")).toHaveText(
        "Tour the top commit",
      );

      const workingRow = overlay.locator(".diff-viewer-commit-list-item.working");
      await expect(workingRow.locator(".working-changes-status")).toHaveText("Clean");
      await expect(workingRow).toHaveAttribute("title", "No working changes");

      await expect(overlay.locator(".diff-viewer-toast-hint")).toHaveCount(0);
      await expect(overlay.getByText("Table of Contents", { exact: true })).toBeVisible();
      const contents = overlay.getByRole("navigation", { name: "Tour contents" });
      const tourContentsScroll = overlay.locator(".diff-viewer-sidebar-tour-scroll");
      const tourView = overlay.locator(".commit-tour-view");
      const overview = contents.getByRole("button", { name: "Overview" });
      await expect(overview).toBeVisible();
      await page.setViewportSize({ width: 1800, height: 6000 });
      await expect
        .poll(() => tourView.evaluate((element) => element.scrollHeight <= element.clientHeight))
        .toBe(true);
      await expect(overview).toHaveAttribute("aria-current", "location");
      await expect
        .poll(() => overview.evaluate((element) => getComputedStyle(element).boxShadow))
        .toBe("none");
      await page.setViewportSize({ width: 1800, height: 900 });
      await expect(overview).toHaveAttribute("aria-current", "location");
      const section = contents.getByRole("button", { name: "Core behavior" });
      await expect(section).toBeVisible();
      const changesTree = contents.getByRole("list", { name: "Changes in Detail 12" });
      await expect(changesTree.locator(".tour-file-tree-directory .diff-tree-label")).toHaveText(
        "src / nested",
      );
      const fileHeading = changesTree.locator(".tour-file-heading");
      await expect(fileHeading).toHaveCount(1);
      await expect(fileHeading).toHaveAccessibleName(
        "Go to first change in src/nested/example.spec.ts",
      );
      const changeRows = changesTree.locator(".tour-change-row");
      const changeButtons = changesTree.locator(".tour-change-link");
      await expect(changeRows).toHaveCount(2);
      await expect(changeButtons).toHaveCount(2);
      for (const button of await changeButtons.all()) {
        await expect(button).toHaveAccessibleName(
          /src\/nested\/example\.spec\.ts · \d+–\d+ \+1 −1$/,
        );
        await expect(button).not.toHaveAttribute("title", /lines/);
        await expect(button.locator(".diff-tree-decoration")).toHaveText(/^L\d+–\d+$/);
        await expect(button.locator(".diff-tree-decoration")).toHaveCSS(
          "background-color",
          "rgba(0, 0, 0, 0)",
        );
        await expect(button.locator(".diff-tree-changes-added")).toHaveText("+1");
        await expect(button.locator(".diff-tree-changes-deleted")).toHaveText("−1");
      }
      await expect(overlay.locator(".commit-tour-chunk-header code").first()).toHaveText(
        /^src\/nested\/example\.spec\.ts · \d+–\d+$/,
      );
      await expect(changesTree.locator(".tour-file-name-stem", { hasText: "example" })).toHaveCount(
        1,
      );
      await expect(
        changesTree.locator(".tour-file-name-suffix", { hasText: ".spec.ts" }),
      ).toHaveCount(1);
      await expect(overlay.locator(".diff-tree")).toHaveCount(0);

      await overlay.locator(".diff-viewer-view-switcher button", { hasText: "Files" }).click();
      await expect(overlay.locator(".diff-viewer-view-switcher button.active")).toHaveText("Files");
      await expect(overlay.locator(".diff-tree")).toBeVisible();
      await expect(overlay.getByRole("navigation", { name: "Tour contents" })).toHaveCount(0);
      await overlay.locator(".diff-viewer-view-switcher button", { hasText: "Tour" }).click();
      await expect(overlay.getByRole("navigation", { name: "Tour contents" })).toBeVisible();
      await expect(overlay.locator(".diff-viewer-toast-hint")).toHaveCount(0);

      const tourDocument = overlay.locator(".commit-tour-document");
      await expect
        .poll(async () => {
          const [viewBox, documentBox] = await Promise.all([
            tourView.boundingBox(),
            tourDocument.boundingBox(),
          ]);
          if (!viewBox || !documentBox) return 0;
          return documentBox.width / viewBox.width;
        })
        .toBeGreaterThan(0.97);

      await section.click();
      await expect
        .poll(() => tourView.evaluate((element) => element.scrollTop))
        .toBeGreaterThan(100);
      await expect(section).toHaveAttribute("aria-current", "location");
      await expect
        .poll(() =>
          overlay.locator("#tour-entry-0").evaluate((target) => {
            const view = target.closest(".commit-tour-view");
            if (!view) return Number.POSITIVE_INFINITY;
            return Math.abs(target.getBoundingClientRect().top - view.getBoundingClientRect().top);
          }),
        )
        .toBeLessThan(40);

      const firstChunk = overlay.locator(".commit-tour-chunk").first();
      const firstChangeButton = changeButtons.first();
      await firstChunk.evaluate((element) => element.scrollIntoView({ block: "start" }));
      await expect(firstChangeButton).toHaveAttribute("aria-current", "location");
      await expect(firstChangeButton.locator(".diff-tree-decoration")).toHaveCSS(
        "background-color",
        "rgba(0, 0, 0, 0)",
      );
      await page.setViewportSize({ width: 1800, height: 600 });
      await expect
        .poll(async () => {
          const [containerBox, buttonBox] = await Promise.all([
            tourContentsScroll.boundingBox(),
            firstChangeButton.boundingBox(),
          ]);
          if (!containerBox || !buttonBox) return false;
          return (
            buttonBox.y >= containerBox.y - 1 &&
            buttonBox.y + buttonBox.height <= containerBox.y + containerBox.height + 1
          );
        })
        .toBe(true);
      await page.setViewportSize({ width: 1800, height: 900 });
      await tourView.evaluate((element) => {
        element.scrollTop = element.scrollHeight;
      });
      const lastChangeButton = changeButtons.nth(1);
      await expect(lastChangeButton).toHaveAttribute("aria-current", "location");
      await expect
        .poll(async () => {
          const [containerBox, buttonBox] = await Promise.all([
            tourContentsScroll.boundingBox(),
            lastChangeButton.boundingBox(),
          ]);
          if (!containerBox || !buttonBox) return false;
          return (
            buttonBox.y >= containerBox.y - 1 &&
            buttonBox.y + buttonBox.height <= containerBox.y + containerBox.height + 1
          );
        })
        .toBe(true);

      const pierreDiff = firstChunk.locator("diffs-container");
      await expect(pierreDiff).toBeVisible();
      await expect
        .poll(() =>
          pierreDiff.evaluate((element) => {
            const pre = element.shadowRoot?.querySelector("pre");
            if (!pre) return null;
            const style = window.getComputedStyle(pre);
            return {
              fontSize: style.fontSize,
              lineHeight: style.lineHeight,
              usesUiMonospace: style.fontFamily.startsWith("ui-monospace"),
            };
          }),
        )
        .toEqual({ fontSize: "12px", lineHeight: "18px", usesUiMonospace: true });
    });
  });
  test("groups hunks by file and separates navigation from trivial visibility", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-tour-navigation-", async (tempDir) => {
      const repo = join(tempDir, "repo");
      mkdirSync(join(repo, "src", "first"), { recursive: true });
      mkdirSync(join(repo, "src", "second"), { recursive: true });
      initGitRepo(repo);

      const firstLines = Array.from({ length: 240 }, (_, index) => `first line ${index + 1}`);
      const secondLines = Array.from({ length: 240 }, (_, index) => `second line ${index + 1}`);
      const firstPath = join(repo, "src", "first", "shared.ts");
      const secondPath = join(repo, "src", "second", "shared.ts");
      writeFileSync(firstPath, firstLines.join("\n") + "\n");
      writeFileSync(secondPath, secondLines.join("\n") + "\n");
      git(repo, "add", "src/first/shared.ts", "src/second/shared.ts");
      git(repo, "commit", "-m", "Navigation base");
      git(repo, "branch", "upstream");
      git(repo, "switch", "-c", "feature");
      git(repo, "branch", "--set-upstream-to=upstream", "feature");

      for (const line of [10, 70, 130]) firstLines[line - 1] = `changed first line ${line}`;
      for (const line of [20, 80, 140, 220]) secondLines[line - 1] = `changed second line ${line}`;
      writeFileSync(firstPath, firstLines.join("\n") + "\n");
      writeFileSync(secondPath, secondLines.join("\n") + "\n");
      git(repo, "add", "src/first/shared.ts", "src/second/shared.ts");
      git(repo, "commit", "-m", "Tour grouped changes");

      const scaffold = JSON.parse(
        execFileSync(shelleyBin, ["tour", "scaffold", "-C", repo, "HEAD"], {
          encoding: "utf8",
        }),
      );
      expect(scaffold.chunks).toHaveLength(7);
      const tour = {
        ...scaffold,
        title: "Grouped tour navigation",
        intro: Array.from(
          { length: 18 },
          (_, index) =>
            `Navigation overview ${index + 1} keeps the trailing chunks below the fold.`,
        ).join("\n\n"),
        chunks: [
          { header: "## Trailing changes" },
          ...scaffold.chunks.map((chunk: { ref: number }, index: number) =>
            index === 0
              ? { ...chunk, comment: "The first change is intentionally expanded." }
              : { ...chunk, trivial: true },
          ),
        ],
      };
      const tourPath = join(tempDir, "tour.json");
      writeFileSync(tourPath, JSON.stringify(tour));
      execFileSync(shelleyBin, ["tour", "attach", "-C", repo, "HEAD", tourPath]);

      const slug = await createConversationViaAPI(request, "Hello", { cwd: repo });
      const overlay = await openDiffViewer(page, slug);
      await expect(overlay.locator(".diff-viewer-view-switcher button.active")).toHaveText("Tour");

      const contents = overlay.getByRole("navigation", { name: "Tour contents" });
      const changesTree = contents.getByRole("list", { name: "Changes in Trailing changes" });
      const fileHeadings = changesTree.locator(".tour-file-heading");
      const changeRows = changesTree.locator(".tour-change-row");
      const changeLinks = changesTree.locator(".tour-change-link");
      const visibilityControls = changeRows.locator(".tour-visibility-control");
      await expect(fileHeadings).toHaveCount(2);
      await expect(changesTree.locator(".tour-file-name")).toHaveCount(2);
      await expect(changeRows).toHaveCount(7);
      await expect(changeLinks).toHaveCount(7);
      await expect(visibilityControls).toHaveCount(7);
      await expect(changeRows.locator("button button")).toHaveCount(0);
      await expect(fileHeadings.nth(0)).toHaveAccessibleName(
        "Go to first change in src/first/shared.ts",
      );
      await expect(fileHeadings.nth(1)).toHaveAccessibleName(
        "Go to first change in src/second/shared.ts",
      );

      for (let index = 0; index < 7; index += 1) {
        const path = index < 3 ? "src/first/shared.ts" : "src/second/shared.ts";
        const state = index === 0 ? "" : " — trivial change \\(hidden\\)";
        await expect(changeLinks.nth(index)).toHaveAccessibleName(
          new RegExp(
            `^${path.replaceAll("/", "\\/").replace(".", "\\.")} · \\d+–\\d+ \\+1 −1${state}$`,
          ),
        );
        await expect(changeLinks.nth(index)).toHaveAttribute(
          "data-tour-target",
          `tour-entry-${index + 1}`,
        );
      }
      await expect(changeRows.first()).not.toHaveClass(/tour-change-collapsed/);
      await expect(changeRows.first().locator("span.tour-visibility-control")).toHaveCount(1);
      await expect(changeRows.first().locator("button.tour-visibility-control")).toHaveCount(0);
      await expect(visibilityControls.first()).not.toHaveAttribute("aria-expanded");
      await expect(visibilityControls.first()).not.toHaveAttribute("aria-controls");
      const visibilityIcons = changeRows.locator(".tour-visibility-icon");
      await expect(visibilityIcons).toHaveCount(7);
      await expect(
        changesTree.locator(".tool-chevron, .tour-change-marker, .tour-trivial-label"),
      ).toHaveCount(0);
      for (const icon of await visibilityIcons.all()) {
        await expect(icon).toHaveAttribute("aria-hidden", "true");
        await expect(icon).toHaveCSS("width", "12px");
        await expect(icon).toHaveCSS("height", "12px");
      }
      await expect(visibilityIcons.first().locator("circle")).toHaveCount(1);
      for (let index = 1; index < 7; index += 1) {
        const path = index < 3 ? "src/first/shared.ts" : "src/second/shared.ts";
        await expect(changeLinks.nth(index)).toHaveAccessibleName(/trivial change \(hidden\)$/);
        await expect(changeRows.nth(index)).toHaveClass(/tour-change-collapsed/);
        await expect(visibilityControls.nth(index)).toHaveAccessibleName(
          new RegExp(
            `^Show ${path.replaceAll("/", "\\/").replace(".", "\\.")} · \\d+–\\d+ \\+1 −1$`,
          ),
        );
        await expect(visibilityControls.nth(index)).toHaveAttribute("aria-expanded", "false");
        await expect(visibilityControls.nth(index)).toHaveAttribute(
          "aria-controls",
          `tour-entry-${index + 1}`,
        );
        await expect(visibilityIcons.nth(index).locator("circle")).toHaveCount(0);
      }

      const tourView = overlay.locator(".commit-tour-view");
      const overview = contents.getByRole("button", { name: "Overview" });
      const firstSecondFileRow = changeRows.nth(3);
      const firstSecondFileLink = changeLinks.nth(3);
      const firstSecondFileEye = visibilityControls.nth(3);
      const firstSecondFileChunk = overlay.locator("#tour-entry-4");
      const firstSecondFileToggle = firstSecondFileChunk.locator(".commit-tour-chunk-toggle");

      await fileHeadings.nth(1).click();
      await expect(firstSecondFileLink).toHaveAccessibleName(/trivial change \(hidden\)$/);
      await expect(firstSecondFileLink).toHaveAttribute("aria-current", "location");
      await expect(firstSecondFileRow).toHaveClass(/tour-change-collapsed/);
      await expect(firstSecondFileRow.locator(".tour-visibility-icon circle")).toHaveCount(0);
      await expect(firstSecondFileToggle).toHaveAttribute("aria-expanded", "false");
      await expect(firstSecondFileChunk.locator(".commit-tour-chunk-body")).toHaveCount(0);

      await fileHeadings.nth(1).press("Enter");
      await expect(firstSecondFileLink).toHaveAttribute("aria-current", "location");
      await expect(firstSecondFileRow).toHaveClass(/tour-change-collapsed/);
      await fileHeadings.nth(1).press("Space");
      await expect(firstSecondFileLink).toHaveAttribute("aria-current", "location");
      await expect(firstSecondFileRow).toHaveClass(/tour-change-collapsed/);

      await firstSecondFileLink.click();
      await expect(firstSecondFileLink).toHaveAttribute("aria-current", "location");
      await expect(firstSecondFileRow).toHaveClass(/tour-change-collapsed/);
      await firstSecondFileLink.locator(".diff-tree-changes-added").click();
      await expect(firstSecondFileLink).toHaveAttribute("aria-current", "location");
      await expect(firstSecondFileRow).toHaveClass(/tour-change-collapsed/);
      await firstSecondFileLink.press("Enter");
      await expect(firstSecondFileLink).toHaveAttribute("aria-current", "location");
      await expect(firstSecondFileRow).toHaveClass(/tour-change-collapsed/);
      await firstSecondFileLink.press("Space");
      await expect(firstSecondFileLink).toHaveAttribute("aria-current", "location");
      await expect(firstSecondFileRow).toHaveClass(/tour-change-collapsed/);
      await expect(firstSecondFileToggle).toHaveAttribute("aria-expanded", "false");

      await firstSecondFileEye.click();
      await expect(firstSecondFileEye).toHaveAccessibleName(/^Hide src\/second\/shared\.ts ·/);
      await expect(firstSecondFileEye).toHaveAttribute("aria-expanded", "true");
      await expect(firstSecondFileLink).toHaveAccessibleName(/trivial change \(shown\)$/);
      await expect(firstSecondFileRow).not.toHaveClass(/tour-change-collapsed/);
      await expect(firstSecondFileRow.locator(".tour-visibility-icon circle")).toHaveCount(1);
      await expect(firstSecondFileToggle).toHaveAttribute("aria-expanded", "true");
      await expect(firstSecondFileChunk.locator(".commit-tour-chunk-body")).toBeVisible();

      await firstSecondFileEye.press("Space");
      await expect(firstSecondFileEye).toHaveAccessibleName(/^Show src\/second\/shared\.ts ·/);
      await expect(firstSecondFileEye).toHaveAttribute("aria-expanded", "false");
      await expect(firstSecondFileLink).toHaveAccessibleName(/trivial change \(hidden\)$/);
      await expect(firstSecondFileRow).toHaveClass(/tour-change-collapsed/);
      await expect(firstSecondFileRow.locator(".tour-visibility-icon circle")).toHaveCount(0);
      await expect(firstSecondFileToggle).toHaveAttribute("aria-expanded", "false");

      await firstSecondFileEye.press("Enter");
      await expect(firstSecondFileEye).toHaveAttribute("aria-expanded", "true");
      await expect(firstSecondFileRow.locator(".tour-visibility-icon circle")).toHaveCount(1);
      await expect(firstSecondFileToggle).toHaveAttribute("aria-expanded", "true");
      await firstSecondFileEye.click();
      await expect(firstSecondFileEye).toHaveAttribute("aria-expanded", "false");
      await expect(firstSecondFileRow.locator(".tour-visibility-icon circle")).toHaveCount(0);
      await expect(firstSecondFileToggle).toHaveAttribute("aria-expanded", "false");

      // Inline and sidebar visibility controls share the same expansion state.
      await firstSecondFileToggle.click();
      await expect(firstSecondFileEye).toHaveAccessibleName(/^Hide src\/second\/shared\.ts ·/);
      await expect(firstSecondFileEye).toHaveAttribute("aria-expanded", "true");
      await expect(firstSecondFileLink).toHaveAccessibleName(/trivial change \(shown\)$/);
      await expect(firstSecondFileRow).not.toHaveClass(/tour-change-collapsed/);
      await expect(firstSecondFileRow.locator(".tour-visibility-icon circle")).toHaveCount(1);
      await firstSecondFileToggle.click();
      await expect(firstSecondFileEye).toHaveAccessibleName(/^Show src\/second\/shared\.ts ·/);
      await expect(firstSecondFileEye).toHaveAttribute("aria-expanded", "false");
      await expect(firstSecondFileLink).toHaveAccessibleName(/trivial change \(hidden\)$/);
      await expect(firstSecondFileRow).toHaveClass(/tour-change-collapsed/);
      await expect(firstSecondFileRow.locator(".tour-visibility-icon circle")).toHaveCount(0);

      const nearEndRow = changeRows.nth(5);
      const nearEndLink = changeLinks.nth(5);
      const nearEndEye = visibilityControls.nth(5);
      const nearEndChunk = overlay.locator("#tour-entry-6");
      const nearEndToggle = nearEndChunk.locator(".commit-tour-chunk-toggle");

      // Navigation to a hidden change scrolls to its collapsed header without revealing it.
      await nearEndLink.click();
      await expect(nearEndLink).toHaveAccessibleName(/trivial change \(hidden\)$/);
      await expect(nearEndLink).toHaveAttribute("aria-current", "location");
      await expect(nearEndRow).toHaveClass(/tour-change-collapsed/);
      await expect(nearEndEye).toHaveAttribute("aria-expanded", "false");
      await expect(nearEndToggle).toHaveAttribute("aria-expanded", "false");
      await expect(nearEndChunk.locator(".commit-tour-chunk-body")).toHaveCount(0);
      await expect
        .poll(async () => {
          const [viewBox, chunkBox] = await Promise.all([
            tourView.boundingBox(),
            nearEndChunk.boundingBox(),
          ]);
          if (!viewBox || !chunkBox) return false;
          return chunkBox.y >= viewBox.y && chunkBox.y < viewBox.y + viewBox.height;
        })
        .toBe(true);

      await overview.click();
      await expect(overview).toHaveAttribute("aria-current", "location");
      await expect
        .poll(() =>
          overlay.locator("#tour-overview").evaluate((target) => {
            const view = target.closest(".commit-tour-view");
            if (!view) return Number.POSITIVE_INFINITY;
            return Math.abs(target.getBoundingClientRect().top - view.getBoundingClientRect().top);
          }),
        )
        .toBeLessThan(40);
      const overviewScrollTop = await tourView.evaluate((element) => element.scrollTop);

      // The eye changes visibility only, even when its chunk is offscreen in the main pane.
      await nearEndEye.click();
      await expect(nearEndEye).toHaveAccessibleName(/^Hide src\/second\/shared\.ts ·/);
      await expect(nearEndEye).toHaveAttribute("aria-expanded", "true");
      await expect(nearEndLink).toHaveAccessibleName(/trivial change \(shown\)$/);
      await expect(nearEndRow).not.toHaveClass(/tour-change-collapsed/);
      await expect(nearEndRow.locator(".tour-visibility-icon circle")).toHaveCount(1);
      await expect(nearEndToggle).toHaveAttribute("aria-expanded", "true");
      await expect(overview).toHaveAttribute("aria-current", "location");
      await tourView.evaluate(
        () =>
          new Promise<void>((resolve) =>
            requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
          ),
      );
      await expect
        .poll(() =>
          tourView.evaluate((element, top) => Math.abs(element.scrollTop - top), overviewScrollTop),
        )
        .toBeLessThan(2);

      // Navigation remains a separate action after the eye reveals the change.
      await nearEndLink.click();
      await expect(nearEndLink).toHaveAttribute("aria-current", "location");
      await expect(nearEndChunk.locator(".commit-tour-chunk-body")).toBeVisible();
      await expect(nearEndChunk.locator("diffs-container")).toBeVisible();
      await expect
        .poll(() =>
          nearEndChunk
            .locator("diffs-container")
            .evaluate((element) => Boolean(element.shadowRoot?.querySelector("pre"))),
        )
        .toBe(true);
      await expect(
        changesTree.getByRole("button", { name: /trivial change \(shown\)$/ }),
      ).toHaveCount(1);
      await expect
        .poll(async () => {
          const [viewBox, chunkBox] = await Promise.all([
            tourView.boundingBox(),
            nearEndChunk.boundingBox(),
          ]);
          if (!viewBox || !chunkBox) return false;
          return chunkBox.y >= viewBox.y && chunkBox.y < viewBox.y + viewBox.height;
        })
        .toBe(true);

      await nearEndLink.click();
      await expect(nearEndLink).toHaveAccessibleName(/trivial change \(shown\)$/);
      await expect(nearEndLink).toHaveAttribute("aria-current", "location");

      // Clicking/selecting text without scrolling must not switch to the last chunk.
      await nearEndChunk.dispatchEvent("pointerdown");
      await tourView.evaluate(
        () =>
          new Promise<void>((resolve) =>
            requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
          ),
      );
      await expect(nearEndLink).toHaveAttribute("aria-current", "location");

      // An inline toggle is fresh reading intent, not another jump to the old TOC target.
      const finalRow = changeRows.nth(6);
      const finalLink = changeLinks.nth(6);
      const finalEye = visibilityControls.nth(6);
      const finalChunk = overlay.locator("#tour-entry-7");
      const lastToggle = finalChunk.locator(".commit-tour-chunk-toggle");
      await expect(lastToggle).toBeInViewport({ ratio: 1 });
      const toggleTop = (await lastToggle.boundingBox())!.y;
      await lastToggle.click();
      await expect(lastToggle).toHaveAttribute("aria-expanded", "true");
      await expect(finalEye).toHaveAttribute("aria-expanded", "true");
      await expect(finalLink).toHaveAccessibleName(/trivial change \(shown\)$/);
      await expect(finalRow).not.toHaveClass(/tour-change-collapsed/);
      await expect(finalRow.locator(".tour-visibility-icon circle")).toHaveCount(1);
      await tourView.evaluate(
        () =>
          new Promise<void>((resolve) =>
            requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
          ),
      );
      await expect
        .poll(async () => Math.abs((await lastToggle.boundingBox())!.y - toggleTop))
        .toBeLessThan(2);
      await lastToggle.click();
      await expect(lastToggle).toHaveAttribute("aria-expanded", "false");
      await expect(finalEye).toHaveAttribute("aria-expanded", "false");
      await expect(finalLink).toHaveAccessibleName(/trivial change \(hidden\)$/);
      await expect(finalRow).toHaveClass(/tour-change-collapsed/);
      await expect(finalRow.locator(".tour-visibility-icon circle")).toHaveCount(0);
      await nearEndLink.click();

      // Tab into the main pane: native focus scrolling must not be pulled back to the jump.
      await finalLink.focus();
      await page.keyboard.press("Tab");
      const diffToggle = overlay.locator(".commit-tour-diff-toggle");
      await expect(diffToggle).toBeFocused();
      await expect(diffToggle).toBeInViewport();
      await expect(overview).toHaveAttribute("aria-current", "location");

      await finalLink.click();
      await expect(finalLink).toHaveAccessibleName(/trivial change \(hidden\)$/);
      await expect(finalLink).toHaveAttribute("aria-current", "location");
      await expect(finalRow).toHaveClass(/tour-change-collapsed/);
      await expect(lastToggle).toHaveAttribute("aria-expanded", "false");
      await expect(finalChunk.locator(".commit-tour-chunk-body")).toHaveCount(0);
      await expect
        .poll(async () => {
          const [viewBox, chunkBox] = await Promise.all([
            tourView.boundingBox(),
            finalChunk.boundingBox(),
          ]);
          if (!viewBox || !chunkBox) return false;
          return (
            chunkBox.y < viewBox.y + viewBox.height && chunkBox.y + chunkBox.height > viewBox.y
          );
        })
        .toBe(true);

      await overview.click();
      await expect(overview).toHaveAttribute("aria-current", "location");
      await finalEye.click();
      await expect(finalEye).toHaveAccessibleName(/^Hide src\/second\/shared\.ts ·/);
      await expect(finalEye).toHaveAttribute("aria-expanded", "true");
      await expect(finalLink).toHaveAccessibleName(/trivial change \(shown\)$/);
      await expect(finalRow).not.toHaveClass(/tour-change-collapsed/);
      await expect(finalRow.locator(".tour-visibility-icon circle")).toHaveCount(1);
      await expect(lastToggle).toHaveAttribute("aria-expanded", "true");
      await expect(overview).toHaveAttribute("aria-current", "location");

      await finalLink.click();
      await expect(finalLink).toHaveAttribute("aria-current", "location");
      await expect(finalChunk.locator(".commit-tour-chunk-body")).toBeVisible();
      await expect(finalChunk.locator("diffs-container")).toBeVisible();
      await expect
        .poll(() =>
          finalChunk
            .locator("diffs-container")
            .evaluate((element) => Boolean(element.shadowRoot?.querySelector("pre"))),
        )
        .toBe(true);
      await expect
        .poll(async () => {
          const [viewBox, chunkBox] = await Promise.all([
            tourView.boundingBox(),
            finalChunk.boundingBox(),
          ]);
          if (!viewBox || !chunkBox) return false;
          return (
            chunkBox.y < viewBox.y + viewBox.height && chunkBox.y + chunkBox.height > viewBox.y
          );
        })
        .toBe(true);
      await expect(finalLink).toHaveAttribute("aria-current", "location");

      await overview.click();
      await expect(overview).toHaveAttribute("aria-current", "location");
      await expect
        .poll(() =>
          overlay.locator("#tour-overview").evaluate((target) => {
            const view = target.closest(".commit-tour-view");
            if (!view) return Number.POSITIVE_INFINITY;
            return Math.abs(target.getBoundingClientRect().top - view.getBoundingClientRect().top);
          }),
        )
        .toBeLessThan(40);

      await overlay.locator(".diff-viewer-view-switcher button", { hasText: "Files" }).click();
      await expect(overlay.locator(".diff-tree")).toBeVisible();
      await overlay.locator(".diff-viewer-view-switcher button", { hasText: "Tour" }).click();
      const restoredContents = overlay.getByRole("navigation", { name: "Tour contents" });
      const restoredRows = restoredContents.locator(".tour-change-row");
      const restoredLinks = restoredContents.locator(".tour-change-link");
      const restoredEyes = restoredRows.locator(".tour-visibility-control");
      await expect(restoredContents.getByRole("button", { name: "Overview" })).toHaveAttribute(
        "aria-current",
        "location",
      );
      await expect(restoredLinks.nth(3)).toHaveAccessibleName(/trivial change \(hidden\)$/);
      await expect(restoredLinks.nth(5)).toHaveAccessibleName(/trivial change \(shown\)$/);
      await expect(restoredLinks.nth(6)).toHaveAccessibleName(/trivial change \(shown\)$/);
      await expect(restoredRows.nth(3)).toHaveClass(/tour-change-collapsed/);
      await expect(restoredRows.nth(5)).not.toHaveClass(/tour-change-collapsed/);
      await expect(restoredRows.nth(6)).not.toHaveClass(/tour-change-collapsed/);
      await expect(restoredEyes.nth(3)).toHaveAttribute("aria-expanded", "false");
      await expect(restoredEyes.nth(5)).toHaveAttribute("aria-expanded", "true");
      await expect(restoredEyes.nth(6)).toHaveAttribute("aria-expanded", "true");
      await expect(restoredRows.nth(3).locator(".tour-visibility-icon circle")).toHaveCount(0);
      await expect(restoredRows.nth(5).locator(".tour-visibility-icon circle")).toHaveCount(1);
      await expect(restoredRows.nth(6).locator(".tour-visibility-icon circle")).toHaveCount(1);
    });
  });
});

test("tour sections collapse independently and navigation reveals their changes", async ({
  page,
  request,
}) => {
  await withTempDir("shelley-tour-collapse-", async (tempDir) => {
    const repo = join(tempDir, "repo");
    mkdirSync(repo);
    initGitRepo(repo);
    for (const file of ["first.txt", "second.txt"]) writeFileSync(join(repo, file), "before\n");
    git(repo, "add", ".");
    git(repo, "commit", "-m", "Base commit");
    for (const file of ["first.txt", "second.txt"]) writeFileSync(join(repo, file), "after\n");
    git(repo, "commit", "-am", "Two sections");
    const scaffold = JSON.parse(
      execFileSync(shelleyBin, ["tour", "scaffold", "-C", repo, "HEAD"], { encoding: "utf8" }),
    );
    expect(scaffold.chunks).toHaveLength(2);
    const shot = join(tempDir, "shot.png");
    writeFileSync(shot, makePNG(200, 100));
    const tourPath = join(tempDir, "tour.json");
    writeFileSync(
      tourPath,
      JSON.stringify({
        ...scaffold,
        chunks: [
          { header: "## First section" },
          { media: shot, comment: "First section preview." },
          { ...scaffold.chunks[0], comment: "First explanation." },
          { header: "## Second section" },
          { ...scaffold.chunks[1], comment: "Second explanation." },
        ],
      }),
    );
    execFileSync(shelleyBin, ["tour", "attach", "-C", repo, "HEAD", tourPath]);
    const slug = await createConversationViaAPI(request, "Hello", { cwd: repo });
    const overlay = await openDiffViewer(page, slug);
    const view = overlay.locator(".commit-tour-view");
    const first = view.locator(".commit-tour-chunk").nth(0);
    const second = view.locator(".commit-tour-chunk").nth(1);
    const media = view.locator(".commit-tour-media");
    await expect(first).toBeVisible();
    await expect(second).toBeVisible();
    await expect(media).toBeVisible();
    await first.getByRole("button", { name: "Show full first.txt", exact: true }).click();
    await expect(
      first.getByRole("button", { name: "Show changes only in first.txt", exact: true }),
    ).toBeVisible();
    await expect(
      view.getByRole("button", { name: "Collapse First section", exact: true }),
    ).toBeVisible();
    await view.getByRole("button", { name: "Collapse First section", exact: true }).click();
    await expect(first).toBeHidden();
    await expect(media).toBeHidden();
    await expect(second).toBeVisible();
    const expand = view.getByRole("button", { name: "Expand First section", exact: true });
    await expect(expand).toHaveAttribute("aria-expanded", "false");
    await expand.focus();
    await page.keyboard.press("Enter");
    await expect(first).toBeVisible();
    await expect(media).toBeVisible();
    await expect(
      first.getByRole("button", { name: "Show changes only in first.txt", exact: true }),
    ).toBeVisible();
    await view.getByRole("button", { name: "Collapse First section", exact: true }).click();
    await view.getByRole("button", { name: "Collapse Second section", exact: true }).click();
    await expect(first).toBeHidden();
    await expect(second).toBeHidden();
    const contents = overlay.getByRole("navigation", { name: "Tour contents" });
    await contents.getByRole("button", { name: /^first.txt ·/ }).click();
    await expect(first).toBeVisible();
    await expect(media).toBeVisible();
    await expect(second).toBeHidden();
    await contents.getByRole("button", { name: "Second section", exact: true }).click();
    await expect(second).toBeVisible();

    await page.setViewportSize({ width: 393, height: 851 });
    await view.getByRole("button", { name: "Collapse First section", exact: true }).click();
    await expect(first).toBeHidden();
    await expect(second).toBeVisible();
    await view.getByRole("button", { name: "Expand First section", exact: true }).click();
    await expect(first).toBeVisible();
  });
});
