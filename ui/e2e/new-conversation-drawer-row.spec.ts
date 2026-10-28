import { test, expect, type Page } from "@playwright/test";
import {
  createConversationViaAPIWithDetails,
  startNewConversationFromHeader,
  testWorkingDirectory,
} from "./helpers";

// A new conversation is a selected draft row in the drawer from the moment it
// opens, and the row stays put through the send: no wait for the draft
// autosave, no blank or missing row while the server catches up.

test.use({ viewport: { width: 1280, height: 720 } });

test.beforeEach(async ({ page }) => {
  // Record the event stream, to tell when the list has reported a row.
  await page.addInitScript(() => {
    const w = window as unknown as { __streamed: string[] };
    w.__streamed = [];
    window.EventSource = class extends window.EventSource {
      constructor(url: string | URL, init?: EventSourceInit) {
        super(url, init);
        this.addEventListener("message", (e) => w.__streamed.push(e.data));
      }
    };
  });
});

async function streamed(page: Page, id: string) {
  await expect
    .poll(() =>
      page.evaluate(
        (id) =>
          (window as unknown as { __streamed: string[] }).__streamed.some((d) => d.includes(id)),
        id,
      ),
    )
    .toBe(true);
}

// Holds the response to the POST creating a conversation: `id` resolves once
// the server has made it, and `release` lets the response through. This plays
// a slow server whose list stream reports the row before the POST answers.
async function holdCreate(page: Page, path: string) {
  let release = () => {};
  const held = new Promise<void>((resolve) => (release = resolve));
  let created: (id: string) => void = () => {};
  const id = new Promise<string>((resolve) => (created = resolve));
  await page.route(`**${path}`, async (route) => {
    const response = await route.fetch();
    created((await response.json()).conversation_id);
    await held;
    await route.fulfill({ response });
  });
  return { id, release };
}

interface Frame {
  shown: boolean;
  title: string;
  preview: string;
  path: string;
}

// The placeholder and the server's row for conversation `id`.
function composerRows(page: Page, id: string) {
  return page.locator(
    `.drawer .conversation-item:is([data-conversation-id="composer"], [data-conversation-id="${id}"])`,
  );
}

function activeRow(page: Page) {
  return page.locator(".drawer .conversation-item.active");
}

// Samples the drawer every frame: whether a row is selected, its title and
// preview, and the URL path. (Not the row's position: parallel tests add
// conversations above it.)
async function recordFrames(page: Page) {
  await page.evaluate(() => {
    const w = window as unknown as { __frames: Frame[] };
    const frames: Frame[] = [];
    w.__frames = frames;
    const tick = () => {
      const row = document.querySelector(".drawer .conversation-item.active");
      frames.push({
        title: row?.querySelector(".conversation-title")?.textContent?.trim() ?? "",
        preview: row?.querySelector(".conversation-preview")?.textContent?.trim() ?? "",
        shown: !!row,
        path: location.pathname,
      });
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  });
}

async function recordedFrames(page: Page) {
  return page.evaluate(() => (window as unknown as { __frames: Frame[] }).__frames);
}

async function openNewConversation(page: Page) {
  await page.goto("/new");
  await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
  await expect(activeRow(page)).toHaveAttribute("data-conversation-id", "composer");
}

test("a new conversation is a selected draft row from the start", async ({ page, request }) => {
  const other = await createConversationViaAPIWithDetails(request, "echo: elsewhere");
  const draft = await holdCreate(page, "/api/conversations/draft");
  // Runs first (routes match newest first): the draft isn't sent until told.
  let sendDraft = () => {};
  const draftHeld = new Promise<void>((resolve) => (sendDraft = resolve));
  await page.route("**/api/conversations/draft", async (route) => {
    await draftHeld;
    await route.fallback();
  });
  await page.goto(`/c/${other.slug}`);
  await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
  const otherRow = page.locator(
    `.drawer .conversation-item[data-conversation-id="${other.conversationId}"]`,
  );
  await expect(otherRow).toHaveClass(/active/);

  await startNewConversationFromHeader(page);
  await expect(activeRow(page)).toHaveAttribute("data-conversation-id", "composer");
  await expect(activeRow(page).locator(".conversation-title")).toHaveText(/^draft( \d+)?$/);

  // Leaving it unused leaves nothing behind, once the pointer leaves the
  // drawer: until then rows stay put under it.
  const placeholder = page.locator('.drawer [data-conversation-id="composer"]');
  await otherRow.click();
  await expect(otherRow).toHaveClass(/active/);
  await expect(placeholder).toHaveCount(1);
  await expect(placeholder).not.toHaveClass(/\bactive\b/);
  await page.mouse.move(900, 400);
  await expect(placeholder).toHaveCount(0);

  await startNewConversationFromHeader(page);
  await expect(activeRow(page)).toHaveAttribute("data-conversation-id", "composer");
  await page.getByTestId("message-input").fill("write a limerick");
  await expect(activeRow(page).locator(".conversation-preview")).toHaveText("write a limerick");

  // The list reports the saved draft before its id comes back: still one row.
  sendDraft();
  const id = await draft.id;
  await streamed(page, id);
  await expect(composerRows(page, id)).toHaveCount(1);
  await expect(activeRow(page).locator(".conversation-preview")).toHaveText("write a limerick");

  draft.release();
  await expect(activeRow(page)).toHaveAttribute("data-conversation-id", id);
  await expect(activeRow(page).locator(".conversation-preview")).toHaveText("write a limerick");
  await expect(page.locator(".drawer .conversation-item.active")).toHaveCount(1);

  // Deleted elsewhere, the draft's row goes rather than turning placeholder.
  expect((await page.request.post(`/api/conversation/${id}/delete`)).ok()).toBe(true);
  await expect(
    page.locator(`.drawer .conversation-item[data-conversation-id="${id}"]`),
  ).toHaveCount(0);
});

// Deletes the row, asking first, the pointer still over the drawer. Returns
// the row that was below it (or else above), which should open.
async function deleteRow(page: Page, id: string) {
  const ids = await page
    .locator(".drawer .conversation-item")
    .evaluateAll((rows) => rows.map((row) => row.getAttribute("data-conversation-id")));
  const row = page.locator(`.drawer .conversation-item[data-conversation-id="${id}"]`);
  await row.getByRole("button", { name: "Delete", exact: true }).click();
  await row.locator(".drawer-delete-confirm").getByRole("button", { name: "Delete" }).click();
  await expect(row).toHaveCount(0);
  return ids[ids.indexOf(id) + 1] ?? ids[ids.indexOf(id) - 1];
}

async function expectOpened(page: Page, id: string | null | undefined) {
  await expect(page.locator(".drawer .conversation-item.active")).toHaveCount(1);
  await expect(activeRow(page)).toHaveAttribute("data-conversation-id", id!);
  await expect(page).toHaveURL(/\/c\//);
}

const deleteButton = (page: Page) =>
  activeRow(page).getByRole("button", { name: "Delete", exact: true });

test("deleting the draft being composed opens its neighbor", async ({ page, request }) => {
  await createConversationViaAPIWithDetails(request, "echo: neighbor");

  // Unused: the placeholder goes at once, rather than lingering.
  await openNewConversation(page);
  await expectOpened(page, await deleteRow(page, "composer"));

  // Typed, it's about to be saved; until it is, there's nothing to delete,
  // even emptied while the save is under way.
  let save = () => {};
  const saving = new Promise<void>((resolve) => (save = resolve));
  await page.route("**/api/conversations/draft", async (route) => {
    await saving;
    await route.fallback();
  });
  await startNewConversationFromHeader(page);
  await expect(deleteButton(page)).toBeVisible();
  const input = page.getByTestId("message-input");
  const requested = page.waitForRequest(
    (r) => r.method() === "POST" && r.url().endsWith("/api/conversations/draft"),
  );
  const created = page.waitForResponse(
    (r) => r.request().method() === "POST" && r.url().endsWith("/api/conversations/draft"),
  );
  await input.fill("never mind");
  await expect(activeRow(page).locator(".conversation-preview")).toHaveText("never mind");
  await expect(deleteButton(page)).toHaveCount(0);
  await requested;
  await input.fill("");
  await expect(activeRow(page).locator(".conversation-preview")).toHaveText("");
  await expect(deleteButton(page)).toHaveCount(0);
  await input.fill("never mind");
  save();

  // Saved: the draft goes, and its text with it.
  const { conversation_id: id } = await (await created).json();
  await expect(activeRow(page)).toHaveAttribute("data-conversation-id", id);
  await expectOpened(page, await deleteRow(page, id));
  await expect(input).toHaveValue("");
  expect((await request.get(`/api/conversation/${id}`)).status()).toBe(404);
  await startNewConversationFromHeader(page);
  await expect(input).toHaveValue("");
});

test("alone, a new conversation has no delete, and a deleted draft returns to it", async ({
  page,
}) => {
  await page.route("**/api/conversations/snapshot", (route) =>
    route.fulfill({ json: { conversations: [], hash: "none" } }),
  );
  await page.route("**/api/stream2**", (route) => route.abort());
  await openNewConversation(page);
  await expect(page.locator(".drawer .conversation-item")).toHaveCount(1);
  await expect(deleteButton(page)).toHaveCount(0);

  const created = page.waitForResponse(
    (r) => r.request().method() === "POST" && r.url().endsWith("/api/conversations/draft"),
  );
  await page.getByTestId("message-input").fill("lonely");
  const { conversation_id: id } = await (await created).json();
  await expect(activeRow(page)).toHaveAttribute("data-conversation-id", id);
  await expect(page).toHaveURL(new RegExp(`/c/${id}$`));
  expect(await deleteRow(page, id)).toBeUndefined();
  await expect(page).toHaveURL(/\/new$/);
  await expect(activeRow(page)).toHaveAttribute("data-conversation-id", "composer");
  await expect(page.getByTestId("message-input")).toHaveValue("");
});

test("a draft row keeps the sent message until the agent replies", async ({ page }) => {
  const message = `echo: drafted ${Date.now()}`;
  const reply = message.slice("echo: ".length);
  await openNewConversation(page);
  const created = page.waitForResponse(
    (r) => r.request().method() === "POST" && r.url().endsWith("/api/conversations/draft"),
  );
  await page.getByTestId("message-input").fill(message);
  const { conversation_id: id } = await (await created).json();
  await expect(activeRow(page)).toHaveAttribute("data-conversation-id", id);

  await recordFrames(page);
  await page.getByTestId("send-button").click();
  await expect(activeRow(page).locator(".conversation-preview")).toHaveText(reply, {
    timeout: 30000,
  });

  const frames = await recordedFrames(page);
  expect(frames.length).toBeGreaterThan(0);
  for (const frame of frames) {
    expect(frame.shown, JSON.stringify(frame)).toBe(true);
    expect([message, reply], JSON.stringify(frame)).toContain(frame.preview);
    expect(frame.path, JSON.stringify(frame)).not.toBe("/");
  }
});

test("a first message sent before the draft is saved keeps a single selected row", async ({
  page,
}) => {
  const message = `echo: undrafted ${Date.now()}`;
  const reply = message.slice("echo: ".length);
  const send = await holdCreate(page, "/api/conversations/new");
  await openNewConversation(page);
  await recordFrames(page);

  // Type and send in one go, before the draft autosave can fire, so no draft
  // exists yet.
  await page.evaluate(async (message) => {
    const input = document.querySelector<HTMLTextAreaElement>('[data-testid="message-input"]')!;
    input.value = message;
    input.dispatchEvent(new Event("input", { bubbles: true }));
    await Promise.resolve(); // Vue renders the enabled send button.
    document.querySelector<HTMLButtonElement>('[data-testid="send-button"]')!.click();
  }, message);
  const id = await send.id;
  await streamed(page, id);
  await expect(composerRows(page, id)).toHaveCount(1);
  await expect(activeRow(page).locator(".conversation-preview")).toHaveText(message);
  send.release();
  await expect(activeRow(page).locator(".conversation-preview")).toHaveText(reply, {
    timeout: 30000,
  });

  // Selected throughout; once it previews the message, never blank.
  const frames = await recordedFrames(page);
  for (const frame of frames) expect(frame.shown, JSON.stringify(frame)).toBe(true);
  for (const frame of frames.slice(frames.findIndex((f) => f.preview))) {
    expect([message, reply], JSON.stringify(frame)).toContain(frame.preview);
  }
  await expect(page.locator(".drawer .conversation-item.active")).toHaveCount(1);
  await expect(page.locator(".drawer .conversation-item").filter({ hasText: reply })).toHaveCount(
    1,
  );
});

test("an unused draft left for other rows stays put until the pointer leaves", async ({
  page,
  request,
}) => {
  const other = await createConversationViaAPIWithDetails(request, "echo: elsewhere");
  const saved = await request.post("/api/conversations/draft", {
    data: { draft: "saved idea", cwd: testWorkingDirectory() },
  });
  expect(saved.ok()).toBe(true);
  const { conversation_id: draftId } = await saved.json();
  await openNewConversation(page);
  const placeholder = page.locator('.drawer [data-conversation-id="composer"]');
  const row = (id: string) =>
    page.locator(`.drawer .conversation-item[data-conversation-id="${id}"]`);

  // Opening a saved draft, then another conversation: the unused one stays.
  await row(draftId).click();
  await expect(row(draftId)).toHaveClass(/active/);
  await expect(placeholder).toHaveCount(1);
  await expect(placeholder).not.toHaveClass(/\bactive\b/);
  await row(other.conversationId).click();
  await expect(row(other.conversationId)).toHaveClass(/active/);
  await expect(placeholder).toHaveCount(1);

  // Clicking it goes back to it.
  await placeholder.click();
  await expect(page).toHaveURL(/\/new$/);
  await expect(activeRow(page)).toHaveAttribute("data-conversation-id", "composer");
  await expect(page.locator(".drawer .conversation-item.active")).toHaveCount(1);
});

test("archiving the only row beside an unused draft returns to the draft", async ({
  page,
  request,
}) => {
  const only = await createConversationViaAPIWithDetails(request, "echo: alone");
  const snapshot = await (await request.get("/api/conversations/snapshot")).json();
  const listed = snapshot.conversations.filter(
    (c: { conversation_id: string }) => c.conversation_id === only.conversationId,
  );
  expect(listed).toHaveLength(1);
  await page.route("**/api/conversations/snapshot", (route) =>
    route.fulfill({ json: { conversations: listed, hash: "only" } }),
  );
  await page.route("**/api/stream2**", (route) => route.abort());
  const composerRequests: string[] = [];
  page.on("request", (r) => {
    if (r.url().includes("/api/conversation/composer")) composerRequests.push(r.url());
  });
  await openNewConversation(page);
  const onlyRow = page.locator(
    `.drawer .conversation-item[data-conversation-id="${only.conversationId}"]`,
  );
  await onlyRow.click();
  await expect(onlyRow).toHaveClass(/active/);

  // Archive from the keyboard, the pointer still over the drawer.
  await onlyRow.getByRole("button", { name: "Actions" }).focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("menuitem", { name: "Archive" })).toBeVisible();
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/new$/);
  await expect(activeRow(page)).toHaveAttribute("data-conversation-id", "composer");
  expect(composerRequests).toEqual([]);
});
