import { expect, test, type Page } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

// Alt+Enter (⌥↵ on mac) queues the composed message to run after the current
// turn instead of interrupting it, and falls back to an ordinary send when the
// agent is idle. Playwright reports a Linux platform, so the mod is Ctrl.

async function openConversation(
  page: Page,
  request: Parameters<typeof createConversationViaAPI>[0],
) {
  const slug = await createConversationViaAPI(request, "echo: queue shortcut seed");
  await page.goto(`/c/${slug}`);
  await page.waitForLoadState("domcontentloaded");
  const messageInput = page.getByTestId("message-input");
  await expect(messageInput).toBeVisible({ timeout: 30000 });
  return messageInput;
}

async function sendAndWaitForWorking(page: Page, text: string) {
  const messageInput = page.getByTestId("message-input");
  await messageInput.fill(text);
  await page.getByTestId("send-button").click();
  await expect(page.getByTestId("agent-thinking")).toBeVisible({ timeout: 30000 });
}

test.describe("queue-shortcut", () => {
  test.use({ viewport: { width: 1280, height: 800 }, isMobile: false, hasTouch: false });

  test("Alt+Enter queues the composed message", async ({ page, request }) => {
    const messageInput = await openConversation(page, request);
    await sendAndWaitForWorking(page, "delay: 60");

    await messageInput.fill("queued via shortcut");
    await messageInput.press("Alt+Enter");

    await expect(messageInput).toHaveValue("");
    await expect(page.getByTestId("queued-badge")).toHaveCount(1, { timeout: 10000 });
    await expect(page.locator("text=queued via shortcut")).toHaveCount(1);
  });

  test("holding Alt previews queueing on the send button", async ({ page, request }) => {
    const messageInput = await openConversation(page, request);
    await sendAndWaitForWorking(page, "delay: 60");
    await messageInput.fill("hold to preview");

    const sendButton = page.getByTestId("send-button");
    await page.keyboard.down("Alt");
    await expect(sendButton).toHaveAttribute("aria-label", "Queue after agent finishes");
    await page.keyboard.up("Alt");
    await expect(sendButton).toHaveAttribute("aria-label", "Send message");
  });

  for (const shortcut of ["Shift+Enter", "Alt+Shift+Enter"]) {
    test(`${shortcut} still inserts a newline while the agent works`, async ({ page, request }) => {
      const messageInput = await openConversation(page, request);
      await sendAndWaitForWorking(page, "delay: 60");

      await messageInput.fill("first line");
      await messageInput.press(shortcut);
      await messageInput.pressSequentially("second line");

      await expect(messageInput).toHaveValue("first line\nsecond line");
      await expect(page.getByTestId("queued-badge")).toHaveCount(0);
    });
  }

  test("Alt+click queues the composed message", async ({ page, request }) => {
    const messageInput = await openConversation(page, request);
    await sendAndWaitForWorking(page, "delay: 60");
    await messageInput.fill("queued via click");
    await page.getByTestId("send-button").click({ modifiers: ["Alt"] });
    await expect(messageInput).toHaveValue("");
    await expect(page.getByTestId("queued-badge")).toHaveCount(1);
    await expect(page.getByText("queued via click", { exact: true })).toHaveCount(1);
  });

  test("Ctrl+Alt does not arm or invoke queueing", async ({ page, request }) => {
    const messageInput = await openConversation(page, request);
    await sendAndWaitForWorking(page, "delay: 60");
    await messageInput.fill("sent with control");
    await page.keyboard.down("Control");
    await page.keyboard.down("Alt");
    await expect(page.getByTestId("send-button")).toHaveAttribute("aria-label", "Send message");
    await page.keyboard.press("Enter");
    await page.keyboard.up("Alt");
    await page.keyboard.up("Control");
    await expect(messageInput).toHaveValue("");
    await expect(page.getByTestId("queued-badge")).toHaveCount(0);
    await expect(page.getByText("sent with control", { exact: true })).toHaveCount(1);
  });

  test("Alt+Enter preserves a pending upload and queues it once ready", async ({ page, request }) => {
    const messageInput = await openConversation(page, request);
    await sendAndWaitForWorking(page, "delay: 60");
    let releaseUpload!: () => void;
    const uploadBlocked = new Promise<void>((resolve) => { releaseUpload = resolve; });
    await page.route("**/api/upload", async (route) => {
      await uploadBlocked;
      await route.fulfill({ json: { path: "/tmp/queue-shortcut.txt" } });
    });
    try {
      await page.locator('input[type="file"]').setInputFiles({
        name: "queue-shortcut.txt",
        mimeType: "text/plain",
        buffer: Buffer.from("queued attachment"),
      });
      await expect(page.locator(".message-attachment-uploading")).toHaveCount(1);
      await messageInput.fill("keep my attachment");
      await messageInput.press("Alt+Enter");
      await expect(messageInput).toHaveValue("keep my attachment");
      await expect(page.locator(".message-attachment-uploading")).toHaveCount(1);
      await expect(page.getByTestId("queued-badge")).toHaveCount(0);
    } finally {
      releaseUpload();
    }
    await expect(page.locator(".message-attachment-ready")).toHaveCount(1);
    const queuedRequest = page.waitForRequest((req) =>
      req.method() === "POST" && req.url().includes("/chat") && req.postDataJSON().queue === true,
    );
    await messageInput.press("Alt+Enter");
    expect((await queuedRequest).postDataJSON().message).toContain("/tmp/queue-shortcut.txt");
    await expect(messageInput).toHaveValue("");
    await expect(page.locator(".message-attachment")).toHaveCount(0);
    await expect(page.getByTestId("queued-badge")).toHaveCount(1);
  });

  test("Alt+Enter sends when the agent is idle", async ({ page, request }) => {
    const messageInput = await openConversation(page, request);

    await messageInput.fill("sent via shortcut");
    await messageInput.press("Alt+Enter");

    await expect(messageInput).toHaveValue("");
    await expect(page.locator("text=sent via shortcut")).toHaveCount(1);
    await expect(page.getByTestId("queued-badge")).toHaveCount(0);
  });
});
