import { expect, test, type Locator, type Page, type Route } from "@playwright/test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import {
  openWorkspaceTool,
  createConversationViaAPI,
  createConversationViaAPIWithDetails,
  git,
  initGitRepo,
  installTranscriptionAvailability,
  withTempDir,
} from "./helpers";

// A repository whose HEAD commit renames one line of a file taller than the
// editor, so only part of it is ever on screen.
function reviewRepo(dir: string): string {
  const repo = join(dir, "repo");
  mkdirSync(join(repo, "src"), { recursive: true });
  initGitRepo(repo);
  const lines = Array.from({ length: 80 }, (_, index) => `const value${index + 1} = ${index + 1};`);
  writeFileSync(join(repo, "src", "app.ts"), lines.join("\n") + "\n");
  git(repo, "add", ".");
  git(repo, "commit", "-m", "Base");
  lines[19] = "const renamedValue = 20;";
  writeFileSync(join(repo, "src", "app.ts"), lines.join("\n") + "\n");
  git(repo, "commit", "-am", "Rename value20");
  return repo;
}

// The predictable-only test server has no transcription route, so it
// refuses /transcription; pass the request on as plain text, which promotes
// a draft all the same.
async function sendAsText(route: Route) {
  const body = route.request().postDataJSON() as Record<string, unknown>;
  await route.continue({ postData: JSON.stringify({ ...body, message: "Narrated review" }) });
}

// A microphone and recorder that produce a few bytes per second.
async function installMicrophone(page: Page, transcription = true) {
  await installTranscriptionAvailability(page, transcription);
  await page.addInitScript(() => {
    class Track extends EventTarget {
      kind = "audio";
      stop() {}
    }
    class Stream {
      tracks = [new Track()];
      getTracks() {
        return this.tracks;
      }
      getAudioTracks() {
        return this.tracks;
      }
      getVideoTracks() {
        return [];
      }
    }
    class Recorder extends EventTarget {
      static isTypeSupported() {
        return true;
      }
      state = "inactive";
      mimeType: string;
      ondataavailable: ((event: Event) => void) | null = null;
      private timer = 0;
      constructor(_stream: unknown, options?: { mimeType?: string }) {
        super();
        this.mimeType = options?.mimeType ?? "audio/webm";
      }
      start(timeslice: number) {
        this.state = "recording";
        this.timer = window.setInterval(() => this.emit("chunk"), timeslice);
      }
      stop() {
        window.clearInterval(this.timer);
        this.state = "inactive";
        this.emit("last");
        setTimeout(() => this.dispatchEvent(new Event("stop")));
      }
      emit(text: string) {
        const event = new Event("dataavailable");
        Object.defineProperty(event, "data", { value: new Blob([text], { type: this.mimeType }) });
        this.ondataavailable?.(event);
      }
    }
    Object.defineProperty(window, "MediaRecorder", { value: Recorder, configurable: true });
    Object.defineProperty(window, "AudioContext", { value: undefined, configurable: true });
    Object.defineProperty(navigator, "mediaDevices", {
      configurable: true,
      value: { getUserMedia: async () => new Stream() },
    });
  });
}

// The viewer opens a clean tree at its top commit, on the commit message.
async function waitForCommit(overlay: Locator) {
  await expect(
    overlay.locator(".editor.modified .view-line", { hasText: "Rename value20" }),
  ).toBeVisible({ timeout: 30_000 });
}

async function openDiffs(page: Page, slug: string, viewport = { width: 1500, height: 900 }) {
  await page.setViewportSize(viewport);
  await page.goto(`/c/${slug}`);
  await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
  await openWorkspaceTool(page, "Diffs");
  const overlay = page.locator(".diff-viewer-overlay");
  await waitForCommit(overlay);
  return overlay;
}

const contentTop = async (overlay: Locator) =>
  (await overlay.locator(".diff-viewer-content").boundingBox())?.y;

// The live status takes over the header's selector area instead of adding a
// strip, so nothing below the header moves.
async function expectInHeader(overlay: Locator, top: number | undefined) {
  await expect(
    overlay.locator(".diff-viewer-header").getByTestId("review-recording-status"),
  ).toBeVisible();
  expect(await contentTop(overlay)).toBe(top);
}

// With nothing pointed at, the status names what is on screen: the one-line
// commit message, which has no old side.
const COMMIT_MESSAGE_ON_SCREEN = /^commit message [0-9a-f]{8} \(new 1\)$/;

// Comment on the first line of the commit message on screen.
async function commentOnCommitMessage(page: Page, overlay: Locator, text: string) {
  await overlay.locator(".editor.modified .view-line", { hasText: "Rename value20" }).click();
  await page.locator(".diff-viewer-comment-input").fill(text);
  await page.getByRole("button", { name: "Add Comment", exact: true }).click();
}

interface Chat {
  message: string;
}

test.describe("Narrated review recording", () => {
  test("captures what was pointed at and selected, then hands it to the conversation", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-review-rec-", async (dir) => {
      const repo = reviewRepo(dir);
      const hash = git(repo, "rev-parse", "HEAD");
      const slug = await createConversationViaAPI(request, "Hello", { cwd: repo });
      await installMicrophone(page);
      const chats: Chat[] = [];
      await page.route("**/api/conversation/*/chat", async (route: Route) => {
        chats.push(route.request().postDataJSON() as Chat);
        await route.fulfill({ status: 202, contentType: "application/json", body: "{}" });
      });
      await page.addInitScript(() => localStorage.setItem("diff-viewer-layout", "sidebar"));
      const overlay = await openDiffs(page, slug);
      const status = overlay.getByTestId("review-recording-status");
      const where = overlay.getByTestId("review-recording-where");
      const said = overlay.getByTestId("review-recording-said");
      const top = await contentTop(overlay);

      await overlay.getByTestId("review-record-start").click();
      await expect(overlay.getByTestId("review-record-stop")).toBeVisible();
      await expect(where).toHaveText(COMMIT_MESSAGE_ON_SCREEN);
      await expectInHeader(overlay, top);
      await expect(overlay.getByTestId("review-recording-bar")).toHaveCount(0);

      // The viewer cannot be closed mid-recording.
      await expect(overlay.locator(".diff-viewer-close")).toBeDisabled();
      await page.keyboard.press("Escape");
      await expect(overlay).toBeVisible();

      // Controls outside the editor are named by their region and label.
      await overlay.getByRole("button", { name: "Next file (>)" }).hover();
      await expect(where).toHaveText(/^Toolbar.*Next file \(>\)"$/);
      const file = overlay.getByRole("treeitem", { name: /app\.ts/ });
      await file.hover();
      await expect(where).toHaveText(/^Changed files.*"app\.ts"$/);
      await file.click();
      const line = overlay.locator(".editor.modified .view-line", { hasText: "renamedValue" });
      await expect(line).toBeVisible();

      // In the editor, the line under the pointer is what counts.
      await line.hover();
      await expect(where).toHaveText(/^Files.*src\/app\.ts new 20$/);
      await expect(said).toHaveText("const renamedValue = 20;");

      await line.hover({ position: { x: 2, y: 8 } });
      await page.mouse.down();
      await line.hover({ position: { x: 160, y: 8 } });
      await page.mouse.up();
      await expect(status).toContainText("Selected");
      await expect(where).toHaveText(/^Files.*src\/app\.ts new 20$/);
      await expect(said).toHaveCount(0);

      await overlay.getByTestId("review-record-stop").click();
      await expect(overlay.getByTestId("review-recording-sent")).toBeVisible();
      await expect(overlay.locator(".diff-viewer-close")).toBeEnabled();
      // Once sent, the header is back.
      await expect(status).toHaveCount(0);
      await expect(overlay.locator(".diff-viewer-header-title")).not.toHaveAttribute("inert");

      expect(chats).toHaveLength(1);
      const audioPath = chats[0].message.replace(/^\/transcription /, "");
      expect(audioPath).toMatch(/^\/.*review-.*\.webm$/);
      expect(readFileSync(audioPath, "utf8")).toMatch(/^(chunk)*last$/);
      const review = JSON.parse(readFileSync(`${audioPath}.review.json`, "utf8"));
      expect(review).toMatchObject({
        version: 1,
        source: "diff-viewer",
        mime_type: "audio/webm;codecs=opus",
      });
      const events = review.events as Array<Record<string, unknown> & { ms: number; type: string }>;
      expect(events.map((event) => event.ms)).toEqual(
        [...events.map((e) => e.ms)].sort((a, b) => a - b),
      );
      const commit = `${hash.slice(0, 8)} Rename value20`;
      expect(events[0]).toMatchObject({
        ms: 0,
        type: "view",
        where: `Files · ${commit} · commit message ${hash.slice(0, 8)}`,
        mode: "files",
        commit: hash,
      });
      expect(events.at(-1)?.type).toBe("stop");
      expect(events).toContainEqual(
        expect.objectContaining({ type: "pointer", where: 'Toolbar › "Next file (>)"' }),
      );
      expect(events).toContainEqual(
        expect.objectContaining({ type: "pointer", where: 'Changed files › "app.ts"' }),
      );
      expect(events).toContainEqual(
        expect.objectContaining({
          type: "view",
          where: `Files · ${commit} · src/app.ts`,
          mode: "files",
          commit: hash,
          file: "src/app.ts",
        }),
      );
      expect(events).toContainEqual(
        expect.objectContaining({
          type: "pointer",
          where: "Files › src/app.ts new 20",
          file: "src/app.ts",
          side: "new",
          line: 20,
          text: "const renamedValue = 20;",
        }),
      );
      expect(events).toContainEqual(
        expect.objectContaining({
          type: "selection",
          where: "Files › src/app.ts new 20",
          file: "src/app.ts",
          side: "new",
          line: 20,
          end_line: 20,
          text: expect.stringContaining("renamed"),
        }),
      );
      expect(events).toContainEqual(
        expect.objectContaining({
          type: "on_screen",
          items: [expect.stringMatching(/^src\/app\.ts \(new 1–\d+, old 1–\d+\)$/)],
        }),
      );
      // The file pick is the only click: stopping is not part of the review.
      expect(events.filter((event) => event.type === "click")).toEqual([
        expect.objectContaining({ where: 'Changed files › "app.ts"' }),
      ]);
    });
  });

  test("offers no narrated review without transcription", async ({ page, request }) => {
    await withTempDir("shelley-review-rec-", async (dir) => {
      const slug = await createConversationViaAPI(request, "Hello", { cwd: reviewRepo(dir) });
      await installMicrophone(page, false);
      const overlay = await openDiffs(page, slug);
      await expect(overlay.getByTestId("review-record-start")).toHaveCount(0);
    });
  });

  test("cancelling throws the recording away", async ({ page, request }) => {
    await withTempDir("shelley-review-rec-", async (dir) => {
      const slug = await createConversationViaAPI(request, "Hello", { cwd: reviewRepo(dir) });
      await installMicrophone(page);
      let uploads = 0;
      page.on("request", (req) => {
        if (req.url().includes("/api/upload/raw") && req.method() === "POST") uploads++;
      });
      const chats: Chat[] = [];
      await page.route("**/api/conversation/*/chat", async (route: Route) => {
        chats.push(route.request().postDataJSON() as Chat);
        await route.fulfill({ status: 202, contentType: "application/json", body: "{}" });
      });
      let overlay = await openDiffs(page, slug);
      const close = overlay.locator(".diff-viewer-close");
      const start = overlay.getByTestId("review-record-start");
      const stop = overlay.getByTestId("review-record-stop");
      const cancel = overlay.getByTestId("review-record-cancel");
      const tooltip = page.locator(".p-tooltip");
      await start.click();
      await expect(stop).toBeVisible();
      // Locked controls look it, and say why in a tooltip that outlasts the
      // running clock's re-renders.
      await expect(close).toBeDisabled();
      await expect(close).toHaveCSS("opacity", "0.35");
      await expect(close).toHaveCSS("cursor", "not-allowed");
      const outlastsTick = async () => {
        const time = await stop.locator("time").textContent();
        await expect(stop.locator("time")).not.toHaveText(time!);
      };
      await close.hover();
      await expect(tooltip).toHaveText("Stop recording to close");
      await outlastsTick();
      await expect(tooltip).toHaveText("Stop recording to close");
      await cancel.hover();
      await expect(tooltip).toHaveText("Stop and discard the recording");
      await outlastsTick();
      await expect(tooltip).toHaveText("Stop and discard the recording");

      // The first click only asks, and a double-click is not an answer.
      await cancel.dblclick();
      await expect(cancel).toHaveText("Discard?");
      await expect(stop).toBeVisible();
      // A second, separate click throws it away.
      await expect(async () => {
        if (await cancel.isVisible()) await cancel.click();
        await expect(start).toBeVisible({ timeout: 1_000 });
      }).toPass();

      await expect(start).toBeEnabled();
      await expect(start).toBeFocused();
      await expect(overlay.getByTestId("review-recording-status")).toHaveCount(0);
      await expect(overlay.getByTestId("review-recording-bar")).toHaveCount(0);
      await expect(close).toBeEnabled();
      await expect(close).toHaveCSS("opacity", "1");
      expect(uploads).toBe(0);
      expect(chats).toHaveLength(0);

      // Nothing was left behind to offer as unsent.
      await page.reload();
      overlay = await openDiffs(page, slug);
      await expect(overlay.getByTestId("review-record-start")).toBeVisible();
      await expect(overlay.getByTestId("review-recording-bar")).toHaveCount(0);
    });
  });

  test("shows the live status in the mobile header", async ({ page, request }) => {
    await withTempDir("shelley-review-rec-", async (dir) => {
      const slug = await createConversationViaAPI(request, "Hello", { cwd: reviewRepo(dir) });
      await installMicrophone(page);
      const overlay = await openDiffs(page, slug, { width: 390, height: 844 });
      const top = await contentTop(overlay);
      await overlay.getByTestId("review-record-start").click();
      await expect(overlay.getByTestId("review-recording-where")).toHaveText(
        COMMIT_MESSAGE_ON_SCREEN,
      );
      await expectInHeader(overlay, top);
      // The pickers are covered rather than squeezed, and cannot be reached.
      await expect(overlay.locator(".diff-viewer-mobile-selectors")).toHaveAttribute("inert", "");
    });
  });

  test("outside a conversation, a recording starts one of its own", async ({ page, request }) => {
    await withTempDir("shelley-review-rec-", async (dir) => {
      const repo = reviewRepo(dir);
      const hash = git(repo, "rev-parse", "HEAD");
      await installMicrophone(page);
      const drafts: Array<Record<string, unknown>> = [];
      await page.route("**/api/conversations/draft", async (route: Route) => {
        drafts.push(route.request().postDataJSON() as Record<string, unknown>);
        await route.continue();
      });
      const chats: Array<{ path: string; body: Record<string, unknown> }> = [];
      await page.route("**/api/conversation/*/chat", async (route: Route) => {
        chats.push({
          path: new URL(route.request().url()).pathname,
          body: route.request().postDataJSON() as Record<string, unknown>,
        });
        await sendAsText(route);
      });
      const failUploads = (route: Route) => route.fulfill({ status: 503, body: "unavailable" });
      await page.route("**/api/upload/raw*", failUploads);

      await page.setViewportSize({ width: 1500, height: 900 });
      const url = `/new?diff=${hash}&cwd=${encodeURIComponent(repo)}`;
      await page.goto(url);
      const overlay = page.locator(".diff-viewer-overlay");
      await waitForCommit(overlay);
      const start = overlay.getByTestId("review-record-start");
      await start.hover();
      await expect(page.locator(".p-tooltip")).toHaveText(
        "Record a narrated review for a new conversation",
      );
      await start.click();
      const stop = overlay.getByTestId("review-record-stop");
      await stop.hover();
      await expect(page.locator(".p-tooltip")).toHaveText("Stop and send to a new conversation");
      await page.waitForFunction(
        () =>
          document.querySelector("[data-testid=review-record-stop] time")?.textContent !== "00:00",
      );
      await stop.click();
      await expect(overlay.getByTestId("review-recording-error")).toBeVisible();
      await expect(overlay.getByTestId("review-recording-pending")).toContainText(
        "for a new conversation",
      );
      // Nothing is made for a recording that hasn't been accepted.
      expect(drafts).toEqual([]);
      await expect(page).toHaveURL(/\/new$/);

      // After a reload the page's draft is out of the picture: the recording
      // gets a conversation of its own, set up as the page was at start.
      await page.unroute("**/api/upload/raw*", failUploads);
      await page.goto(url);
      await overlay.getByTestId("review-recording-send").click();
      await expect(overlay.getByTestId("review-recording-sent")).toBeVisible();
      expect(drafts).toEqual([
        expect.objectContaining({ draft: "", cwd: repo, model: expect.any(String) }),
      ]);
      expect(chats).toHaveLength(1);
      expect(chats[0].body).toMatchObject({
        message: expect.stringMatching(/^\/transcription \/.*review-.*\.webm$/),
        cwd: repo,
        model: drafts[0].model,
      });
      // Sent, the page moves to the new conversation behind the viewer.
      const id = chats[0].path.split("/")[3];
      // A new conversation's URL waits for its title, so check the drawer.
      await expect(page.locator(`.conversation-item[data-conversation-id="${id}"]`)).toHaveClass(
        /active/,
      );
      const conversation = await request.get(`/api/conversation-by-slug/${id}`);
      expect(await conversation.json()).toMatchObject({ is_draft: false, cwd: repo });
      await start.hover();
      await expect(page.locator(".p-tooltip")).toHaveText("Record a narrated review");
    });
  });

  test("a comment made while recording on the new page shares the recording's conversation", async ({
    page,
  }) => {
    await withTempDir("shelley-review-rec-", async (dir) => {
      const repo = reviewRepo(dir);
      const hash = git(repo, "rev-parse", "HEAD");
      await installMicrophone(page);
      const drafts: Array<Record<string, unknown>> = [];
      await page.route("**/api/conversations/draft", async (route: Route) => {
        drafts.push(route.request().postDataJSON() as Record<string, unknown>);
        await route.continue();
      });
      let accept = false;
      const chats: string[] = [];
      await page.route("**/api/conversation/*/chat", async (route: Route) => {
        chats.push(new URL(route.request().url()).pathname);
        if (accept) await sendAsText(route);
        else await route.fulfill({ status: 503, body: "unavailable" });
      });

      await page.setViewportSize({ width: 1500, height: 900 });
      await page.goto(`/new?diff=${hash}&cwd=${encodeURIComponent(repo)}`);
      const overlay = page.locator(".diff-viewer-overlay");
      await waitForCommit(overlay);
      await overlay.getByTestId("review-record-start").click();
      await expect(overlay.getByTestId("review-record-stop")).toBeVisible();

      // The comment lands in the composer, which saves it as the page's draft.
      await commentOnCommitMessage(page, overlay, "Keep the old name.");
      const input = page.getByTestId("message-input");
      await expect(input).toHaveValue(/Keep the old name\.\n*$/);
      await expect(page).toHaveURL(/\/c\/[^/?]+$/);
      const draftId = page.url().split("/c/")[1];
      expect(drafts).toHaveLength(1);

      await page.waitForFunction(
        () =>
          document.querySelector("[data-testid=review-record-stop] time")?.textContent !== "00:00",
      );
      await overlay.getByTestId("review-record-stop").click();
      await expect(overlay.getByTestId("review-recording-error")).toContainText("unavailable");
      accept = true;
      await overlay.getByTestId("review-recording-send").click();
      await expect(overlay.getByTestId("review-recording-sent")).toBeVisible();
      // Both attempts went to the page's draft; no other was made.
      expect(chats).toEqual([
        `/api/conversation/${draftId}/chat`,
        `/api/conversation/${draftId}/chat`,
      ]);
      expect(drafts).toHaveLength(1);
      await expect(
        page.locator(`.conversation-item[data-conversation-id="${draftId}"]`),
      ).toHaveClass(/active/);
      await expect(input).toHaveValue(/Keep the old name\.\n*$/);
    });
  });

  test("a reload mid-recording still sends it to the draft a comment made", async ({ page }) => {
    await withTempDir("shelley-review-rec-", async (dir) => {
      const repo = reviewRepo(dir);
      const hash = git(repo, "rev-parse", "HEAD");
      await installMicrophone(page);
      const drafts: string[] = [];
      await page.route("**/api/conversations/draft", async (route: Route) => {
        const response = await route.fetch();
        drafts.push(((await response.json()) as { conversation_id: string }).conversation_id);
        await route.fulfill({ response });
      });
      const chats: string[] = [];
      page.on("request", (req) => {
        if (/\/api\/conversation\/[^/]+\/chat$/.test(new URL(req.url()).pathname)) {
          chats.push(new URL(req.url()).pathname);
        }
      });
      await page.route("**/api/conversation/*/chat", sendAsText);

      await page.setViewportSize({ width: 1500, height: 900 });
      await page.goto(`/new?diff=${hash}&cwd=${encodeURIComponent(repo)}`);
      const overlay = page.locator(".diff-viewer-overlay");
      await waitForCommit(overlay);
      await overlay.getByTestId("review-record-start").click();
      await expect(overlay.getByTestId("review-record-stop")).toBeVisible();
      await commentOnCommitMessage(page, overlay, "Keep the old name.");
      await expect(page).toHaveURL(/\/c\/[^/?]+$/);
      expect(drafts).toHaveLength(1);
      const draftId = drafts[0]!;

      // The saved recording names the draft while it is still recording.
      await expect
        .poll(() =>
          page.evaluate(
            () =>
              new Promise<unknown>((resolve, reject) => {
                const open = indexedDB.open("shelley-review-recordings");
                open.onerror = () => reject(open.error);
                open.onsuccess = () => {
                  const all = open.result.transaction("sessions").objectStore("sessions").getAll();
                  all.onsuccess = () => {
                    open.result.close();
                    resolve(all.result.map((session) => session.conversationId));
                  };
                };
              }),
          ),
        )
        .toEqual([draftId]);
      await page.goto(`/c/${draftId}?diff=${hash}&cwd=${encodeURIComponent(repo)}`);
      await expect(overlay.getByTestId("review-recording-pending")).toContainText(
        "Unsent recording",
      );
      await overlay.getByTestId("review-recording-send").click();
      await expect(overlay.getByTestId("review-recording-sent")).toBeVisible();
      expect(chats).toEqual([`/api/conversation/${draftId}/chat`]);
      expect(drafts).toHaveLength(1);
    });
  });

  test("a recording sent from a live conversation leaves the view there", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-review-rec-", async (dir) => {
      const repo = reviewRepo(dir);
      const hash = git(repo, "rev-parse", "HEAD");
      const live = await createConversationViaAPIWithDetails(request, "Hello", { cwd: repo });
      await installMicrophone(page);
      const chats: string[] = [];
      page.on("request", (req) => {
        if (/\/api\/conversation\/[^/]+\/chat$/.test(new URL(req.url()).pathname)) {
          chats.push(new URL(req.url()).pathname);
        }
      });
      await page.route("**/api/conversation/*/chat", sendAsText);
      const failUploads = (route: Route) => route.fulfill({ status: 503, body: "unavailable" });
      await page.route("**/api/upload/raw*", failUploads);

      await page.setViewportSize({ width: 1500, height: 900 });
      await page.goto(`/new?diff=${hash}&cwd=${encodeURIComponent(repo)}`);
      const overlay = page.locator(".diff-viewer-overlay");
      await waitForCommit(overlay);
      await overlay.getByTestId("review-record-start").click();
      await page.waitForFunction(
        () =>
          document.querySelector("[data-testid=review-record-stop] time")?.textContent !== "00:00",
      );
      await overlay.getByTestId("review-record-stop").click();
      await expect(overlay.getByTestId("review-recording-error")).toBeVisible();

      await page.unroute("**/api/upload/raw*", failUploads);
      await page.goto(`/c/${live.slug}?diff=${hash}&cwd=${encodeURIComponent(repo)}`);
      await overlay.getByTestId("review-recording-send").click();
      await expect(overlay.getByTestId("review-recording-sent")).toBeVisible();
      expect(chats).toHaveLength(1);
      const id = chats[0]!.split("/")[3]!;
      expect(id).not.toBe(live.conversationId);
      // The new conversation shows up, but the view stays where the user is.
      await expect(page.locator(`.conversation-item[data-conversation-id="${id}"]`)).toBeVisible();
      await expect(
        page.locator(`.conversation-item[data-conversation-id="${live.conversationId}"]`),
      ).toHaveClass(/active/);
      await expect(page).toHaveURL(new RegExp(`/c/${live.slug}$`));
    });
  });

  test("a recording whose draft was deleted starts another", async ({ page, request }) => {
    await withTempDir("shelley-review-rec-", async (dir) => {
      const repo = reviewRepo(dir);
      const hash = git(repo, "rev-parse", "HEAD");
      await installMicrophone(page);
      const drafts: string[] = [];
      await page.route("**/api/conversations/draft", async (route: Route) => {
        const response = await route.fetch();
        drafts.push(((await response.json()) as { conversation_id: string }).conversation_id);
        await route.fulfill({ response });
      });
      let accept = false;
      const chats: string[] = [];
      await page.route("**/api/conversation/*/chat", async (route: Route) => {
        chats.push(new URL(route.request().url()).pathname);
        if (accept) await sendAsText(route);
        else await route.fulfill({ status: 503, body: "unavailable" });
      });

      await page.setViewportSize({ width: 1500, height: 900 });
      await page.goto(`/new?diff=${hash}&cwd=${encodeURIComponent(repo)}`);
      const overlay = page.locator(".diff-viewer-overlay");
      await waitForCommit(overlay);
      await overlay.getByTestId("review-record-start").click();
      await page.waitForFunction(
        () =>
          document.querySelector("[data-testid=review-record-stop] time")?.textContent !== "00:00",
      );
      await overlay.getByTestId("review-record-stop").click();
      await expect(overlay.getByTestId("review-recording-error")).toContainText("unavailable");
      expect(drafts).toHaveLength(1);
      const deleted = await request.post(`/api/conversation/${drafts[0]}/delete`);
      expect(deleted.ok()).toBeTruthy();

      // One click: the gone draft is noticed and replaced.
      accept = true;
      await overlay.getByTestId("review-recording-send").click();
      await expect(overlay.getByTestId("review-recording-sent")).toBeVisible();
      expect(drafts).toHaveLength(2);
      // Checked before sending: nothing more goes to the deleted draft.
      expect(chats).toEqual([
        `/api/conversation/${drafts[0]}/chat`,
        `/api/conversation/${drafts[1]}/chat`,
      ]);
      const conversation = await request.get(`/api/conversation-by-slug/${drafts[1]}`);
      expect(await conversation.json()).toMatchObject({ is_draft: false, cwd: repo });
      await expect(
        page.locator(`.conversation-item[data-conversation-id="${drafts[1]}"]`),
      ).toHaveClass(/active/);
    });
  });

  test("a recording on an archived conversation starts a new one", async ({ page, request }) => {
    await withTempDir("shelley-review-rec-", async (dir) => {
      const repo = reviewRepo(dir);
      const hash = git(repo, "rev-parse", "HEAD");
      const archived = await createConversationViaAPIWithDetails(request, "Hello", { cwd: repo });
      const response = await request.post(`/api/conversation/${archived.conversationId}/archive`);
      expect(response.ok()).toBeTruthy();
      await installMicrophone(page);
      const chats: string[] = [];
      page.on("request", (req) => {
        if (/\/api\/conversation\/[^/]+\/chat$/.test(new URL(req.url()).pathname)) {
          chats.push(new URL(req.url()).pathname);
        }
      });
      await page.route("**/api/conversation/*/chat", sendAsText);

      await page.setViewportSize({ width: 1500, height: 900 });
      await page.goto(`/c/${archived.slug}?diff=${hash}&cwd=${encodeURIComponent(repo)}`);
      const overlay = page.locator(".diff-viewer-overlay");
      await waitForCommit(overlay);
      const start = overlay.getByTestId("review-record-start");
      await start.hover();
      await expect(page.locator(".p-tooltip")).toHaveText(
        "Record a narrated review for a new conversation",
      );
      await start.click();
      await page.waitForFunction(
        () =>
          document.querySelector("[data-testid=review-record-stop] time")?.textContent !== "00:00",
      );
      await overlay.getByTestId("review-record-stop").click();
      await expect(overlay.getByTestId("review-recording-sent")).toBeVisible();

      expect(chats).toHaveLength(1);
      const id = chats[0]!.split("/")[3]!;
      expect(id).not.toBe(archived.conversationId);
      const started = await request.get(`/api/conversation-by-slug/${id}`);
      expect(await started.json()).toMatchObject({ is_draft: false, cwd: repo, archived: false });
      await expect(page.locator(`.conversation-item[data-conversation-id="${id}"]`)).toHaveClass(
        /active/,
      );
    });
  });

  test("a recording on a draft goes to it after a reload and keeps its text", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-review-rec-", async (dir) => {
      const repo = reviewRepo(dir);
      const hash = git(repo, "rev-parse", "HEAD");
      const created = await request.post("/api/conversations/draft", {
        data: { draft: "Written elsewhere.", model: "predictable", cwd: repo },
      });
      expect(created.ok()).toBeTruthy();
      const draftId = ((await created.json()) as { conversation_id: string }).conversation_id;
      await installMicrophone(page);
      const chats: Array<{ path: string; body: Record<string, unknown> }> = [];
      await page.route("**/api/conversation/*/chat", async (route: Route) => {
        chats.push({
          path: new URL(route.request().url()).pathname,
          body: route.request().postDataJSON() as Record<string, unknown>,
        });
        await sendAsText(route);
      });
      const failUploads = (route: Route) => route.fulfill({ status: 503, body: "unavailable" });
      await page.route("**/api/upload/raw*", failUploads);

      await page.setViewportSize({ width: 1500, height: 900 });
      const url = `/c/${draftId}?diff=${hash}&cwd=${encodeURIComponent(repo)}`;
      await page.goto(url);
      const overlay = page.locator(".diff-viewer-overlay");
      await waitForCommit(overlay);
      await overlay.getByTestId("review-record-start").click();
      await page.waitForFunction(
        () =>
          document.querySelector("[data-testid=review-record-stop] time")?.textContent !== "00:00",
      );
      await overlay.getByTestId("review-record-stop").click();
      await expect(overlay.getByTestId("review-recording-error")).toBeVisible();

      // The draft it began on still takes it after a reload.
      await page.unroute("**/api/upload/raw*", failUploads);
      await page.goto(url);
      await overlay.getByTestId("review-recording-send").click();
      await expect(overlay.getByTestId("review-recording-sent")).toBeVisible();
      expect(chats).toHaveLength(1);
      expect(chats[0].path).toBe(`/api/conversation/${draftId}/chat`);
      expect(chats[0].body).toMatchObject({ model: "predictable", cwd: repo });
      const promoted = await request.get(`/api/conversation-by-slug/${draftId}`);
      expect(await promoted.json()).toMatchObject({ is_draft: false });
      // Its unsent text stays in the composer.
      await overlay.locator(".diff-viewer-close:visible").click();
      await expect(page.getByTestId("message-input")).toHaveValue("Written elsewhere.");
    });
  });

  test("discarding a recording deletes the empty draft it made", async ({ page, request }) => {
    await withTempDir("shelley-review-rec-", async (dir) => {
      const repo = reviewRepo(dir);
      const hash = git(repo, "rev-parse", "HEAD");
      const archived = await createConversationViaAPIWithDetails(request, "Hello", { cwd: repo });
      const response = await request.post(`/api/conversation/${archived.conversationId}/archive`);
      expect(response.ok()).toBeTruthy();
      await installMicrophone(page);
      const chats: string[] = [];
      await page.route("**/api/conversation/*/chat", (route: Route) => {
        chats.push(new URL(route.request().url()).pathname);
        return route.fulfill({ status: 503, body: "unavailable" });
      });

      await page.setViewportSize({ width: 1500, height: 900 });
      await page.goto(`/c/${archived.slug}?diff=${hash}&cwd=${encodeURIComponent(repo)}`);
      const overlay = page.locator(".diff-viewer-overlay");
      await waitForCommit(overlay);
      await overlay.getByTestId("review-record-start").click();
      await page.waitForFunction(
        () =>
          document.querySelector("[data-testid=review-record-stop] time")?.textContent !== "00:00",
      );
      await overlay.getByTestId("review-record-stop").click();
      await expect(overlay.getByTestId("review-recording-error")).toBeVisible();
      expect(chats).toHaveLength(1);
      const id = chats[0]!.split("/")[3]!;
      expect(id).not.toBe(archived.conversationId);
      expect((await request.get(`/api/conversation-by-slug/${id}`)).ok()).toBeTruthy();

      const discard = overlay.getByTestId("review-recording-discard");
      await expect(async () => {
        if (await discard.isVisible()) await discard.click();
        await expect(overlay.getByTestId("review-recording-bar")).toHaveCount(0, {
          timeout: 1_000,
        });
      }).toPass();
      await expect
        .poll(async () => (await request.get(`/api/conversation-by-slug/${id}`)).status())
        .toBe(404);
    });
  });

  test("keeps an unsent recording across reloads until the conversation accepts it", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-review-rec-", async (dir) => {
      const slug = await createConversationViaAPI(request, "Hello", { cwd: reviewRepo(dir) });
      await installMicrophone(page);
      let uploads = 0;
      page.on("request", (req) => {
        if (req.url().includes("/api/upload/raw") && req.method() === "POST") uploads++;
      });
      let accept = false;
      const chats: Chat[] = [];
      await page.route("**/api/conversation/*/chat", async (route: Route) => {
        if (!accept) {
          await route.fulfill({ status: 503, body: "unavailable" });
          return;
        }
        chats.push(route.request().postDataJSON() as Chat);
        await route.fulfill({ status: 202, contentType: "application/json", body: "{}" });
      });

      let overlay = await openDiffs(page, slug);
      await overlay.getByTestId("review-record-start").click();
      await expect(overlay.getByTestId("review-record-stop")).toBeVisible();
      await page.waitForFunction(
        () =>
          document.querySelector("[data-testid=review-record-stop] time")?.textContent !== "00:00",
      );
      await overlay.getByTestId("review-record-stop").click();
      await expect(overlay.getByTestId("review-recording-error")).toContainText("unavailable");
      await expect(overlay.getByTestId("review-recording-pending")).toHaveCount(1);
      expect(uploads).toBe(2);

      await page.reload();
      overlay = await openDiffs(page, slug);
      await expect(overlay.getByTestId("review-recording-pending")).toContainText(
        "Unsent recording",
      );

      // A new recording leaves the unsent one in place, waiting its turn.
      accept = true;
      const top = await contentTop(overlay);
      await overlay.getByTestId("review-record-start").click();
      await expectInHeader(overlay, top);
      await expect(overlay.getByTestId("review-recording-send")).toBeDisabled();
      await overlay.getByTestId("review-record-stop").click();
      await expect(overlay.getByTestId("review-recording-sent")).toBeVisible();
      await expect(overlay.getByTestId("review-recording-pending")).toHaveCount(1);
      expect(uploads).toBe(4);
      expect(chats).toHaveLength(1);

      await overlay.getByTestId("review-recording-send").click();
      await expect(overlay.getByTestId("review-recording-pending")).toHaveCount(0);
      // Both files were already on the server; only the handoff was retried.
      expect(uploads).toBe(4);
      expect(chats).toHaveLength(2);
      for (const chat of chats)
        expect(chat.message).toMatch(/^\/transcription \/.*review-.*\.webm$/);

      await page.reload();
      overlay = await openDiffs(page, slug);
      await expect(overlay.getByTestId("review-recording-bar")).toHaveCount(0);
    });
  });
});
