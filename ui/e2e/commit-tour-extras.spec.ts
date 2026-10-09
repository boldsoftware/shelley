import { expect, test, type Locator, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { openWorkspaceTool, createConversationViaAPI, makePNG, withTempDir } from "./helpers";

// Tours can carry key design decisions, questions for the reader, and
// screenshots or recordings. Each is commentable, and every comment lands in
// the message input: answers quote their question, screenshots open the same
// image annotation view as conversation images, and recordings are commented
// on at the current playback time.

const shelleyBin = resolve(fileURLToPath(new URL("../../bin/shelley", import.meta.url)));

// A one-second 16x16 VP8 WebM, small enough to inline.
const TINY_WEBM =
  "GkXfo59ChoEBQveBAULygQRC84EIQoKEd2VibUKHgQJChYECGFOAZwEAAAAAAAJBEU2bdLpNu4tTq4QVSalmU6yBoU" +
  "27i1OrhBZUrmtTrIHYTbuMU6uEElTDZ1OsggEeTbuMU6uEHFO7a1OsggIr7AEAAAAAAABZAAAAAAAAAAAAAAAAAAAA" +
  "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" +
  "AAAAAAAAAVSalmsirXsYMPQkBNgI1MYXZmNjAuMTYuMTAwV0GNTGF2ZjYwLjE2LjEwMESJiECPQAAAAAAAFlSua8Gu" +
  "AQAAAAAAADjXgQFzxYj2SZdxK2GKIZyBACK1nIN1bmSIgQCGhVZfVlA4g4EBI+ODhA7msoDgibCBELqBEJqBAhJUw2" +
  "f8c3OgY8CAZ8iaRaOHRU5DT0RFUkSHjUxhdmY2MC4xNi4xMDBzc9ZjwItjxYj2SZdxK2GKIWfIoUWjh0VOQ09ERVJE" +
  "h5RMYXZjNjAuMzEuMTAyIGxpYnZweGfIoUWjiERVUkFUSU9ORIeTMDA6MDA6MDEuMDAwMDAwMDAwAB9DtnVAhueBAK" +
  "O8gQAAgLACAJ0BKhAAEAAARwiFhYiFhIgCAgJ1qgP4Agz9KAD+/00S//xYV/FhX8WFf/FhX/z8zu3F/OYAo5WBAPoA" +
  "sQEAARAQABgAGFgv9AAIAACjlYEB9ACxAQABEBAAGAAYWC/0AAgAAKOVgQLuALEBAAEQEAAYABhYL/QACAAAHFO7a5" +
  "G7j7OBALeK94EB8YIBn/CBAw==";

function git(cwd: string, ...args: string[]): string {
  return execFileSync("git", args, { cwd, encoding: "utf8" }).trim();
}

async function openDiffViewer(page: Page, slug: string): Promise<Locator> {
  await page.setViewportSize({ width: 1600, height: 900 });
  await page.addInitScript(() => localStorage.setItem("diff-viewer-layout", "sidebar"));
  await page.goto(`/c/${slug}`);
  await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
  await openWorkspaceTool(page, "Diffs");
  const overlay = page.locator(".diff-viewer-overlay");
  await expect(overlay).toBeVisible({ timeout: 30000 });
  return overlay;
}

test("tour decisions, questions, and media are shown and commentable", async ({
  page,
  request,
}) => {
  await withTempDir("shelley-tour-extras-", async (tempDir) => {
    const repo = join(tempDir, "repo");
    mkdirSync(repo);
    git(repo, "init", "-q");
    git(repo, "config", "user.name", "Tour Test");
    git(repo, "config", "user.email", "tour@example.com");
    writeFileSync(join(repo, "page.html"), "<button>Go</button>\n");
    git(repo, "add", "page.html");
    git(
      repo,
      "-c",
      "core.hooksPath=/dev/null",
      "commit",
      "-q",
      "-m",
      "Add page\n\nPrompt: tour extras",
    );

    const shot = join(tempDir, "shot.png");
    const demo = join(tempDir, "demo.webm");
    writeFileSync(shot, makePNG(200, 100));
    writeFileSync(demo, Buffer.from(TINY_WEBM, "base64"));
    const tourPath = join(tempDir, "tour.json");
    writeFileSync(
      tourPath,
      JSON.stringify({
        version: 1,
        title: "Add a page",
        decisions: [{ title: "Pin media in git", body: "So the note stands alone." }],
        questions: [{ title: "Should `demo` autoplay?", body: "It is short." }],
        chunks: [
          { header: "## What it looks like" },
          { media: shot, comment: "The new page." },
          { media: demo, comment: "Clicking through it." },
          { ref: 0, comment: "The markup." },
        ],
      }),
    );
    execFileSync(shelleyBin, ["tour", "attach", "-C", repo, "HEAD", tourPath]);
    const stored = JSON.parse(
      execFileSync(shelleyBin, ["tour", "show", "-C", repo, "HEAD"], { encoding: "utf8" }),
    );
    const shotBlob: string = stored.chunks[1].blob.slice(0, 12);
    const demoBlob: string = stored.chunks[2].blob.slice(0, 12);
    const short = git(repo, "rev-parse", "--short=8", "HEAD");

    const slug = await createConversationViaAPI(request, "Hello", { cwd: repo });
    const overlay = await openDiffViewer(page, slug);
    const input = page.getByTestId("message-input");
    const where = page.locator(".diff-viewer-comment-dialog-handle");
    const commentInput = page.locator(".diff-viewer-comment-input");
    const addComment = page.getByRole("button", { name: "Add Comment", exact: true });

    const contents = overlay.getByRole("navigation", { name: "Tour contents" });
    await expect(contents.getByRole("button", { name: "Key design decisions" })).toBeVisible();
    await expect(contents.getByRole("button", { name: "Questions (1)" })).toBeVisible();
    await expect(contents.getByRole("button", { name: "Image: shot.png" })).toBeVisible();
    await expect(contents.getByRole("button", { name: "Video: demo.webm" })).toBeVisible();
    await expect(overlay.locator(".commit-tour-items-decision .commit-tour-item-title")).toHaveText(
      "Pin media in git",
    );
    // Titles are inline markdown.
    await expect(
      overlay.locator(".commit-tour-items-question .commit-tour-item-title code"),
    ).toHaveText("demo");

    // Answering a question quotes it.
    await overlay.getByRole("button", { name: "Answer question 1" }).click();
    await expect(where).toHaveText("Add Comment (Question 1)");
    await commentInput.fill("No, keep it paused.");
    await addComment.click();
    await expect(input).toHaveValue(
      `> commit ${short} question 1: Should \`demo\` autoplay?\nNo, keep it paused.\n\n`,
    );

    // Screenshots open the shared image annotation view over the diff viewer.
    const image = overlay.locator(".commit-tour-media img");
    await expect.poll(() => image.evaluate((img: HTMLImageElement) => img.naturalWidth)).toBe(200);
    await contents.getByRole("button", { name: "Image: shot.png" }).click();
    await image.click();
    const annotate = page.locator(".image-comment-overlay");
    await expect(annotate).toBeVisible();
    await expect(annotate.locator(".image-comment-title")).toHaveText(
      `shot.png (git blob ${shotBlob})`,
    );
    await expect(annotate.locator(".image-comment-hint")).toHaveText(/Drag a box/);
    const frame = await annotate.locator(".image-comment-frame").boundingBox();
    expect(frame).not.toBeNull();
    await page.mouse.move(frame!.x + frame!.width * 0.25, frame!.y + frame!.height * 0.25);
    await page.mouse.down();
    await page.mouse.move(frame!.x + frame!.width * 0.75, frame!.y + frame!.height * 0.75, {
      steps: 5,
    });
    await page.mouse.up();
    await expect(commentInput).toBeFocused();
    await commentInput.fill("Make the button bigger.");
    await addComment.click();
    await expect(input).toHaveValue(
      new RegExp(
        `> image shot\\.png \\(git blob ${shotBlob}\\) \\[region \\d+x\\d+\\+\\d+\\+\\d+ of 200x100\\]\\nMake the button bigger\\.\\n\\n$`,
      ),
    );
    // Escape closes the annotation view, not the diff viewer beneath it.
    await page.keyboard.press("Escape");
    await expect(annotate).toHaveCount(0);
    await expect(overlay).toBeVisible();

    // Recordings are commented on at the current playback time.
    const video = overlay.locator(".commit-tour-media video");
    await expect
      .poll(() => video.evaluate((v: HTMLVideoElement) => v.readyState))
      .toBeGreaterThan(0);
    await overlay.getByRole("button", { name: "Comment on demo.webm" }).click();
    await expect(where).toHaveText("Add Comment (demo.webm at 0:00.0)");
    await commentInput.fill("Too fast.");
    await addComment.click();
    await expect(input).toHaveValue(
      new RegExp(`> video demo\\.webm \\(git blob ${demoBlob}\\) at 0:00\\.0\\nToo fast\\.\\n\\n$`),
    );

    // A collapsed section must not leave a recording playing without controls.
    await video.evaluate(async (player: HTMLVideoElement) => {
      player.muted = true;
      player.loop = true;
      await player.play();
    });
    await expect
      .poll(() => video.evaluate((player: HTMLVideoElement) => player.paused))
      .toBe(false);
    await overlay.getByRole("button", { name: "Collapse What it looks like", exact: true }).click();
    await expect(video).toBeHidden();
    await expect.poll(() => video.evaluate((player: HTMLVideoElement) => player.paused)).toBe(true);
    const pausedAt = await video.evaluate((player: HTMLVideoElement) => player.currentTime);
    await overlay.getByRole("button", { name: "Expand What it looks like", exact: true }).click();
    await expect(video).toBeVisible();
    expect(await video.evaluate((player: HTMLVideoElement) => player.paused)).toBe(true);
    expect(await video.evaluate((player: HTMLVideoElement) => player.currentTime)).toBe(pausedAt);
  });
});
