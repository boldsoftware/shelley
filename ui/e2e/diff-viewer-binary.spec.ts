import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import { readFileSync, unlinkSync, writeFileSync } from "node:fs";
import {
  createConversationViaAPI,
  git,
  initGitRepo,
  openWorkspaceTool,
  withTempDir,
} from "./helpers";

const imageBefore = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAIAAACQkWg2AAAACXBIWXMAAAABAAAAAQBPJcTWAAAAF0lEQVR4nGP4y8BAEiJN9aiGUQ1DSgMAfsz9ATa/hGgAAAAASUVORK5CYII=",
  "base64",
);
const imageAfter = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAIAAACQkWg2AAAACXBIWXMAAAABAAAAAQBPJcTWAAAAFUlEQVR4nGNgqGcgDY1qGNUwfDUAAIZufwE07jNxAAAAAElFTkSuQmCC",
  "base64",
);
const audio = Buffer.from(
  "SUQzBAAAAAAAI1RTU0UAAAAPAAADTGF2ZjYwLjE2LjEwMAAAAAAAAAAAAAAA/+M4wAAAAAAAAAAAAEluZm8AAAAPAAAABgAAAogAcXFxcXFxcXFxcXFxcXFxcY6Ojo6Ojo6Ojo6Ojo6Ojo6OqqqqqqqqqqqqqqqqqqqqqsfHx8fHx8fHx8fHx8fHx8fH4+Pj4+Pj4+Pj4+Pj4+Pj4+P/////////////////////AAAAAExhdmM2MC4zMQAAAAAAAAAAAAAAACQDcAAAAAAAAAKIwPmwWQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/+MYxAAM+ALeWUEAApJJCKNtwAfB8Hz8QAgGMHwffKAgGPUXB8H+UBD/wcOYgB/gR3D/R/8EAQB8HwfB8CAg7D44bDYd/xqI/+MYxAcNsPKgAYfQAKPDb/B8Og9AF/4k7xcIEJQ6cu75LLDBHB6FRfi+QioLuuymMxmtTUwK/4SBoSnQav/////6y6wtQAIA/+MYxAsM6OZMAdYwAADAGGADA2EQDCOAwDBeJYDHON4DqSssDJcN8DDEAsYgAIBg8YzICAxt/////+pNy4XC0AuAEBwawMA4/+MYxBINYOpIAVYwAEEDDCEkDD8L4DG4I4Du8SADJqJECoHgMBwBgMBgBBjTJQ4IxQEEkhsxEbDo7/UcG5Ia+7tEYIgjAE6J/+MYxBcS8XakyYegAJQG8BFsBYoXyqLmHyGogDBRZirL01M20qDuJ0ipYKaZ5X//z5eMTBRoFm/+syDIoaGN//Wlaq8zPqZn/+MYxAYNCP20AcYYAdVrz5nKqvJxJLTSMsSyq8zMsaAQmAYBEnIgQEKMKAiYBEjoroILkNiv//4N6bkqTEFNRTMuMTAwqqqq",
  "base64",
);
const binary = Buffer.from([0, 1, 255]);

async function openFixture(page: Page, request: APIRequestContext, cwd: string) {
  initGitRepo(cwd);
  writeFileSync(`${cwd}/image.png`, imageBefore);
  writeFileSync(`${cwd}/tone.mp3`, audio);
  writeFileSync(`${cwd}/opaque.bin`, binary);
  writeFileSync(`${cwd}/deleted.bin`, binary);
  writeFileSync(`${cwd}/text.dat`, "before\n");
  git(cwd, "add", ".");
  git(cwd, "commit", "-m", "binary diff fixtures");
  writeFileSync(`${cwd}/image.png`, imageAfter);
  writeFileSync(`${cwd}/tone.mp3`, Buffer.concat([audio, Buffer.alloc(8)]));
  writeFileSync(`${cwd}/opaque.bin`, Buffer.from([0, 2, 255]));
  writeFileSync(`${cwd}/added.bin`, binary);
  writeFileSync(`${cwd}/text.dat`, "after\n");
  unlinkSync(`${cwd}/deleted.bin`);
  const slug = await createConversationViaAPI(request, "Hello", { cwd });
  await page.goto(`/c/${slug}`);
  await openWorkspaceTool(page, "Diffs");
  const overlay = page.locator(".diff-viewer-overlay");
  await expect(overlay).toBeVisible();
  return overlay;
}

test.describe("Binary file diffs", () => {
  test("binary contents are opt-in and remain read-only in edit mode", async ({
    page,
    request,
  }) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    await withTempDir("shelley-binary-diff-", async (cwd) => {
      const overlay = await openFixture(page, request, cwd);
      const files = overlay.locator("select.diff-viewer-select");
      await files.selectOption("opaque.bin");
      const panel = overlay.getByTestId("binary-file-panel");
      await expect(panel).toBeVisible();
      await expect(overlay.locator(".diff-viewer-binary-toolbar")).toHaveText("View as text");
      await expect(overlay.locator(".diff-viewer-editor")).toBeHidden();
      await expect(panel.getByRole("link", { name: "Download", exact: true })).toHaveCount(2);
      for (const side of await panel.locator(".diff-viewer-binary-side-content").all()) {
        expect(
          await side.locator(".diff-viewer-binary-line + .diff-viewer-binary-download").count(),
        ).toBe(1);
      }
      await expect(
        overlay.getByRole("button", { name: "Next change (.)", exact: true }),
      ).toBeDisabled();
      await overlay.getByRole("button", { name: "View as text", exact: true }).click();
      const editor = overlay.locator(".diff-viewer-editor");
      await expect(editor).toBeVisible();
      await expect(panel).toBeHidden();
      await expect(overlay.locator(".diff-viewer-binary-toolbar")).toHaveText("Binary view");
      await overlay.getByRole("button", { name: "Edit mode", exact: true }).click();
      const modifiedLines = editor.locator(".editor.modified .view-lines");
      await modifiedLines.click();
      await page.keyboard.insertText("must-not-write");
      await expect(modifiedLines).not.toContainText("must-not-write");
      await page.keyboard.press("Control+s");
      expect(readFileSync(`${cwd}/opaque.bin`)).toEqual(Buffer.from([0, 2, 255]));
      await overlay.getByRole("button", { name: "Binary view", exact: true }).click();
      await expect(panel).toBeVisible();
      await files.selectOption("text.dat");
      await expect(panel).toBeHidden();
      await expect(modifiedLines).toContainText("after");
      await modifiedLines.click();
      await page.keyboard.insertText("editable");
      await expect(modifiedLines).toContainText("editable");
      const saved = page.waitForResponse(
        (response) =>
          response.url().includes("/api/write-file") && response.request().method() === "POST",
      );
      await page.keyboard.press("Control+s");
      expect((await saved).ok()).toBeTruthy();
      expect(readFileSync(`${cwd}/text.dat`, "utf8")).toContain("editable");
      await files.selectOption("opaque.bin");
      await expect(panel).toBeVisible();
      await expect(editor).toBeHidden();
    });
  });

  test("images and audio have native previews without download links", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-binary-media-", async (cwd) => {
      const overlay = await openFixture(page, request, cwd);
      const files = overlay.locator("select.diff-viewer-select");
      await files.selectOption("image.png");
      const panel = overlay.getByTestId("binary-file-panel");
      const images = panel.locator("img");
      await expect(images).toHaveCount(2);
      for (const image of await images.all()) {
        await expect
          .poll(() => image.evaluate((img: HTMLImageElement) => img.naturalWidth))
          .toBe(16);
        const topInset = await image.evaluate(
          (img) => img.getBoundingClientRect().top - img.parentElement!.getBoundingClientRect().top,
        );
        expect(topInset).toBeCloseTo(12, 0);
      }
      await expect(panel.getByRole("link", { name: "Download", exact: true })).toHaveCount(0);
      await expect(panel.locator(".diff-viewer-binary-side-header")).toHaveCount(0);
      await files.selectOption("tone.mp3");
      const players = panel.locator("audio");
      await expect(players).toHaveCount(2);
      for (const player of await players.all()) {
        await expect
          .poll(() => player.evaluate((audio: HTMLAudioElement) => audio.duration))
          .toBeGreaterThan(0);
        await expect(player).toHaveAttribute("controls", "");
        expect(
          await player.evaluate((audio) => getComputedStyle(audio.parentElement!).paddingTop),
        ).toBe("12px");
      }
      await expect(panel.getByRole("link", { name: "Download", exact: true })).toHaveCount(0);
    });
  });

  test("added and deleted binaries shrink and hatch the missing side like text diffs", async ({
    page,
    request,
  }) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    await withTempDir("shelley-binary-presence-", async (cwd) => {
      const overlay = await openFixture(page, request, cwd);
      const files = overlay.locator("select.diff-viewer-select");
      const panel = overlay.getByTestId("binary-file-panel");
      for (const name of ["added.bin", "deleted.bin"]) {
        await files.selectOption(name);
        await expect(panel).toBeVisible();
        const missing = panel.locator(".diff-viewer-binary-side").nth(name === "added.bin" ? 0 : 1);
        await expect(missing).toHaveClass(/diff-viewer-binary-side-empty/);
        await expect(missing).toBeEmpty();
        const dimensions = await missing.evaluate((el) => ({
          width: el.getBoundingClientRect().width,
          total: el.parentElement!.getBoundingClientRect().width,
          fill: getComputedStyle(el).backgroundImage,
        }));
        expect(dimensions.width / dimensions.total).toBeCloseTo(0.1, 2);
        expect(dimensions.fill).toContain("linear-gradient");
        await expect(panel.getByRole("link", { name: "Download", exact: true })).toHaveCount(1);
      }
    });
  });
});
