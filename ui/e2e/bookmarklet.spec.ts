import { expect, test, type BrowserContext, type Page } from "@playwright/test";
import { patchInitData } from "./helpers";
import { installMediaMocks } from "./mediaMocks";

// A page on another origin, where the user clicks the bookmarklet.
const HOST = "http://host.test/";
const EVIL = "http://evil.test/";
const HOST_HTML = `<!doctype html><title>Host Page</title>
<main><h1>Host Page</h1><p id="para">The quick brown fox jumps over the lazy dog.</p>
<ul class="list"><li>First item</li><li>Second item</li></ul></main>`;

type Message = { tag: string; type: string; event?: { type: string; where?: string } };
declare global {
  interface Window {
    __bookmarkletMessages: Message[];
    __opened?: Window | null;
  }
}

// Takes the bookmarklet from page's Shelley (or the standalone page), clicks
// it on the host page with its first paragraph selected, and returns both
// windows.
async function openFromBookmarklet(page: Page, context: BrowserContext, standalone = false) {
  await installMediaMocks(context);
  // As if the bookmarklet feature flag were on.
  await patchInitData(context, { bookmarklet: true });
  await context.addInitScript(() => {
    window.__bookmarkletMessages = [];
    window.addEventListener("message", (event) => {
      if (event.data?.tag === "shelley-bookmarklet") window.__bookmarkletMessages.push(event.data);
    });
  });
  await context.route(`${HOST}**`, (route) =>
    route.fulfill({ contentType: "text/html", body: HOST_HTML }),
  );
  let href: string | null;
  if (standalone) {
    await page.goto("/shelley-bookmarklet.html");
    href = await page.locator("#link").getAttribute("href");
  } else {
    await page.goto("/new");
    await page.getByRole("button", { name: "More options" }).click();
    // The menu item is the bookmarklet too: drag it, or click it for the modal.
    const item = page.getByTestId("bookmarklet-menu-item");
    href = await item.getAttribute("href");
    await item.click();
    await expect(page.getByTestId("bookmarklet-link")).toHaveAttribute("href", href!);
  }
  expect(href).toMatch(/^javascript:/);

  const host = await context.newPage();
  await host.goto(HOST);
  await host.evaluate(() => getSelection()!.selectAllChildren(document.getElementById("para")!));
  const opened = host.waitForEvent("popup");
  // As if clicked in the bookmarks bar.
  await host.evaluate((url) => (location.href = url), href!);
  const shelley = await opened;
  const toolbar = host.locator("shelley-bookmarklet");
  await expect(toolbar.getByText("Linked", { exact: true })).toBeVisible();
  return { host, shelley, toolbar, input: shelley.getByTestId("message-input") };
}

// Records, started by start(), and fakes the uploads and the send.
async function recordWhileClicking(
  shelley: Page,
  host: Page,
  start: () => Promise<void>,
  reviewUploads: string[] = [],
) {
  const uploads = new Map<string, string>();
  await shelley.route("**/api/upload/raw?filename=*", async (route) => {
    const filename = new URL(route.request().url()).searchParams.get("filename")!;
    uploads.set(filename, route.request().postData() ?? "");
    const stored = filename.endsWith(".review.json") ? reviewUploads.shift() : undefined;
    await route.fulfill({ json: { path: stored ?? `/tmp/shelley-uploads/${filename}` } });
  });
  const sent: string[] = [];
  await shelley.route("**/api/conversation/*/chat", async (route) => {
    sent.push((route.request().postDataJSON() as { message: string }).message);
    await route.fulfill({ status: 202, json: { status: "queued" } });
  });
  await start();
  await expect(shelley.locator(".recording-status")).toHaveAttribute("data-state", "recording");
  const toolbar = host.locator("shelley-bookmarklet");
  await expect(toolbar.getByText("Recording page")).toBeVisible();
  await expect(toolbar.getByRole("button", { name: "Record" })).toBeDisabled();
  await expect(shelley.getByTestId("bookmarklet-status")).toHaveText(
    "Recording what you do on host.test",
  );
  await host.locator("#para").click();
  await expect
    .poll(() =>
      shelley.evaluate(() =>
        window.__bookmarkletMessages.some((message) => message.event?.type === "click"),
      ),
    )
    .toBe(true);
  await shelley.getByTestId("recording-stop-button").click();
  return { uploads, sent };
}

function reviewOf(uploads: Map<string, string>, audio: string) {
  return JSON.parse(uploads.get(`${audio}.review.json`)!) as {
    source: string;
    url: string;
    title: string;
    duration_ms: number;
    events: Array<{ ms: number; type: string; where?: string; text?: string }>;
  };
}

test("the bookmarklet opens a linked chat about the page", async ({ page, context }) => {
  const { host, shelley, toolbar, input } = await openFromBookmarklet(page, context);
  await expect(input).toHaveValue(
    "Page: Host Page — http://host.test/\n> The quick brown fox jumps over the lazy dog.",
  );
  await expect(shelley).toHaveURL(/\/new$/);
  await expect(shelley.getByTestId("bookmarklet-status")).toHaveText("Linked to host.test");

  // Pick an element into the chat.
  await toolbar.getByRole("button", { name: "Pick" }).click();
  await host.locator("li").nth(1).click();
  await expect(input).toHaveValue(
    /lazy dog\.\n\nPicked on http:\/\/host\.test\/: `body > main > ul\.list > li:nth-of-type\(2\)`\n```html\n<li>Second item<\/li>\n```$/,
  );

  // Record from the page: it reports what the user does there meanwhile.
  const { uploads, sent } = await recordWhileClicking(shelley, host, async () => {
    await toolbar.getByRole("button", { name: "Record" }).click();
    await shelley.getByTestId("bookmarklet-ask").getByRole("button", { name: "Allow" }).click();
  });
  await expect(shelley.getByTestId("recording-panel")).toHaveCount(0);
  await expect(toolbar.getByText("Linked", { exact: true })).toBeVisible();
  await expect(toolbar.getByRole("button", { name: "Record" })).toBeEnabled();
  await expect(shelley.getByTestId("bookmarklet-status")).toHaveText("Linked to host.test");

  const audio = [...uploads.keys()].find((name) => name.endsWith(".webm"))!;
  expect([...uploads.keys()]).toEqual([audio, `${audio}.review.json`]);
  expect(sent).toHaveLength(1);
  expect(sent[0]).toMatch(new RegExp(`^/transcription /tmp/shelley-uploads/${audio}`));
  const review = reviewOf(uploads, audio);
  expect(review).toMatchObject({ source: "web-page", url: HOST, title: "Host Page" });
  expect(review.events).toContainEqual(
    expect.objectContaining({
      type: "click",
      where: "#para",
      text: expect.stringContaining("fox"),
    }),
  );
  expect(review.events.at(-1)).toEqual({ ms: review.duration_ms, type: "stop" });
  for (const event of review.events) {
    expect(event.ms).toBeGreaterThanOrEqual(0);
    expect(event.ms).toBeLessThanOrEqual(review.duration_ms);
  }
});

test("a retry uploads a fresh pair when the review's name was taken", async ({ page, context }) => {
  const { host, shelley } = await openFromBookmarklet(page, context);
  // The first attempt stored the review, but its response was lost.
  const { uploads, sent } = await recordWhileClicking(
    shelley,
    host,
    () => shelley.getByTestId("voice-button").click(),
    ["/tmp/shelley-uploads/taken_0123.review.json"],
  );
  await expect(shelley.getByTestId("recording-error")).toBeVisible();
  expect(sent).toHaveLength(0);
  const first = [...uploads.keys()].find((name) => name.endsWith(".webm"))!;
  uploads.clear();
  await shelley.getByTestId("recording-retry-button").click();
  await expect(shelley.getByTestId("recording-panel")).toHaveCount(0);
  expect([...uploads.keys()]).toEqual([first, `${first}.review.json`]);
  expect(sent).toHaveLength(1);
  expect(reviewOf(uploads, first).source).toBe("web-page");
});

test("Shelley takes picks only from its opener, and picking skips the toolbar", async ({
  page,
  context,
}) => {
  const { host, shelley, toolbar, input } = await openFromBookmarklet(page, context);
  await expect(input).toHaveValue(/^Page: Host Page — http:\/\/host\.test\/\n/);
  // A pick from Shelley's own origin (not the linked page) is dropped.
  await shelley.evaluate(() =>
    window.postMessage(
      {
        tag: "shelley-bookmarklet",
        type: "pick",
        pick: { url: "https://evil.test/", selector: "x", html: "x" },
      },
      "*",
    ),
  );
  const pick = toolbar.getByRole("button", { name: "Pick" });
  await pick.click();
  await host.locator("h1").click();
  await expect(input).toHaveValue(/Picked on http:\/\/host\.test\/: `body > main > h1`/);
  await expect(input).not.toHaveValue(/evil/);
  const before = await input.inputValue();

  // Clicking the toolbar while picking works the toolbar.
  await pick.click();
  await expect(pick).toHaveAttribute("aria-pressed", "true");
  await pick.click();
  await expect(pick).toHaveAttribute("aria-pressed", "false");
  await pick.click();
  await toolbar.getByRole("button", { name: "Close" }).click();
  await expect(toolbar).toHaveCount(0);
  await host.locator("h1").click();
  await expect(input).toHaveValue(before);
});

test("Shelley starts recordings only for sites the user allowed", async ({ page, context }) => {
  const { host, shelley, toolbar } = await openFromBookmarklet(page, context, true);
  const record = toolbar.getByRole("button", { name: "Record" });
  const ask = shelley.getByTestId("bookmarklet-ask");
  const panel = shelley.getByTestId("recording-panel");
  await record.click();
  await expect(ask).toContainText("Let http://host.test start recordings?");
  // Not at once, so it can't be slipped under a click meant for the page.
  await expect(ask.getByRole("button", { name: "Allow" })).toBeDisabled();
  await expect(panel).toHaveCount(0);
  await ask.getByRole("button", { name: "Not now" }).click();
  await expect(ask).toHaveCount(0);
  await expect(shelley.getByTestId("bookmarklet-status")).toHaveText("Linked to host.test");
  await expect(panel).toHaveCount(0);

  await record.click();
  await ask.getByRole("button", { name: "Allow" }).click();
  await expect(panel).toBeVisible();
  await shelley.getByTestId("recording-cancel-button").click();
  await expect(panel).toHaveCount(0);
  // Allowed, it records straight away.
  await record.click();
  await expect(panel).toBeVisible();
  await expect(ask).toHaveCount(0);
  await shelley.getByTestId("recording-cancel-button").click();
  await expect(panel).toHaveCount(0);

  // Another site can open Shelley about host.test, but isn't host.test.
  await context.route(`${EVIL}**`, (route) =>
    route.fulfill({ contentType: "text/html", body: "<title>Evil</title>" }),
  );
  const evil = await context.newPage();
  await evil.goto(EVIL);
  const opened = evil.waitForEvent("popup");
  const url = `${new URL(shelley.url()).origin}/new#bookmarklet=${encodeURIComponent(
    JSON.stringify({ url: HOST, title: "Host Page", selection: "" }),
  )}`;
  await evil.evaluate((url) => (window.__opened = window.open(url, "_blank", "popup")), url);
  const forged = await opened;
  await expect(forged.getByTestId("message-input")).toHaveValue(/^Page: Host Page/);
  await evil.evaluate(() => {
    const pick = { url: location.href, selector: "h1", html: "<h1>Evil</h1>" };
    window.__opened!.postMessage({ tag: "shelley-bookmarklet", type: "record" }, "*");
    window.__opened!.postMessage({ tag: "shelley-bookmarklet", type: "pick", pick }, "*");
  });
  // Once the pick arrives, Shelley has handled (and ignored) the record.
  await expect
    .poll(() => forged.evaluate(() => window.__bookmarkletMessages.some((m) => m.type === "pick")))
    .toBe(true);
  await expect(forged.getByTestId("bookmarklet-status")).toHaveText("From host.test");
  await expect(forged.getByTestId("recording-panel")).toHaveCount(0);
  await forged.close();

  // Not behind a dialog, where the user couldn't see or stop it.
  await shelley.getByRole("button", { name: "More options" }).click();
  await shelley.getByTestId("bookmarklet-menu-item").click();
  await record.click();
  // The pick comes after the record: once it's in, the record was handled.
  await toolbar.getByRole("button", { name: "Pick" }).click();
  await host.locator("h1").click();
  await expect(shelley.getByTestId("message-input")).toHaveValue(/Picked on http:\/\/host\.test\//);
  await expect(panel).toHaveCount(0);

  // Forgotten, it asks again.
  const sites = shelley.getByTestId("bookmarklet-sites");
  await expect(sites).toHaveText(/^http:\/\/host\.test\s*Remove$/);
  await sites.getByRole("button", { name: "Remove" }).click();
  await expect(sites).toHaveCount(0);
  await shelley.keyboard.press("Escape");
  await record.click();
  await expect(ask).toBeVisible();
  await expect(panel).toHaveCount(0);
});

test("without the flag, there's no bookmarklet and Shelley ignores its chats", async ({ page }) => {
  const context = { url: HOST, title: "Host Page", selection: "" };
  await page.goto(`/new#bookmarklet=${encodeURIComponent(JSON.stringify(context))}`);
  await page.getByRole("button", { name: "More options" }).click();
  await expect(page.getByText("MCP Servers")).toBeVisible();
  await expect(page.getByTestId("bookmarklet-menu-item")).toHaveCount(0);
  await expect(page.getByTestId("message-input")).toHaveValue("");
  await expect(page.getByTestId("bookmarklet-status")).toHaveCount(0);
});
