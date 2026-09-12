import { expect, test, type Page } from "@playwright/test";
import { createConversationViaAPIWithDetails, setPageFeatureFlag } from "./helpers";

async function openThresholdConversation(page: Page, conversationId: string, slug: string) {
  await setPageFeatureFlag(page, "compact-send-thresholds", true);
  await page.route(`**/api/conversation/${conversationId}`, async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({ response, json: { ...body, context_window_size: 200_000 } });
  });
  await page.route("**/api/stream2*", (route) => route.abort());
  await page.goto(`/c/${slug}`);
  await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30_000 });
  await expect(page.getByTestId("send-button")).toHaveAttribute("aria-label", "Compact and send");
}

test.describe("compact-send-thresholds", () => {
  test.use({
    viewport: { width: 1280, height: 800 },
    isMobile: false,
    hasTouch: false,
  });

  test("Enter compacts and queues at a context threshold", async ({ page, request }) => {
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "echo: compact send seed",
    );
    let compactRequests = 0;
    const chatRequests: Array<{ message?: string; queue?: boolean }> = [];
    await page.route("**/api/conversations/distill-new-generation", async (route) => {
      compactRequests++;
      await route.fulfill({ json: { conversation_id: conversationId, current_generation: 2 } });
    });
    await page.route(`**/api/conversation/${conversationId}/chat`, async (route) => {
      chatRequests.push(route.request().postDataJSON());
      await route.fulfill({ json: {} });
    });

    await openThresholdConversation(page, conversationId, slug);
    await page.getByTestId("message-input").fill("queued after compaction");
    await page.getByTestId("message-input").press("Enter");

    await expect.poll(() => compactRequests).toBe(1);
    await expect
      .poll(() => chatRequests)
      .toEqual([{ message: "queued after compaction", model: "predictable", queue: true }]);
  });

  test("Send remains selected within the current threshold", async ({ page, request }) => {
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "echo: send override seed",
    );
    let compactRequests = 0;
    const chatRequests: Array<{ message?: string; queue?: boolean }> = [];
    await page.route("**/api/conversations/distill-new-generation", async (route) => {
      compactRequests++;
      await route.fulfill({ json: { conversation_id: conversationId, current_generation: 2 } });
    });
    await page.route(`**/api/conversation/${conversationId}/chat`, async (route) => {
      chatRequests.push(route.request().postDataJSON());
      await route.fulfill({ json: {} });
    });

    await openThresholdConversation(page, conversationId, slug);
    const input = page.getByTestId("message-input");
    const sendButton = page.getByTestId("send-button");
    await input.fill("explicit send");
    await page.getByTestId("send-options-button").click();
    await page.getByTestId("send-option").click();
    await expect(sendButton).toHaveAttribute("aria-label", "Send message");
    await expect(input).toHaveValue("");

    await input.fill("send still selected");
    await expect(sendButton).toBeEnabled();
    await input.press("Enter");
    await expect
      .poll(() => chatRequests.map(({ message, queue }) => ({ message, queue })))
      .toEqual([
        { message: "explicit send", queue: undefined },
        { message: "send still selected", queue: undefined },
      ]);
    expect(compactRequests).toBe(0);
  });

  test("slash and shell commands use ordinary Send", async ({ page, request }) => {
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "echo: command seed",
    );
    let compactRequests = 0;
    const chatRequests: Array<{ message?: string; queue?: boolean }> = [];
    await page.route("**/api/conversations/distill-new-generation", async (route) => {
      compactRequests++;
      await route.fulfill({ json: { conversation_id: conversationId, current_generation: 2 } });
    });
    await page.route(`**/api/conversation/${conversationId}/chat`, async (route) => {
      chatRequests.push(route.request().postDataJSON());
      await route.fulfill({ json: {} });
    });

    await openThresholdConversation(page, conversationId, slug);
    const input = page.getByTestId("message-input");
    const sendButton = page.getByTestId("send-button");

    await input.fill("/model predictable");
    await expect(sendButton).toHaveAttribute("aria-label", "Send message");
    await input.press("Enter");
    await expect.poll(() => chatRequests).toEqual([
      { message: "/model predictable", model: "predictable" },
    ]);

    await input.fill("!printf compact-send-shell");
    await expect(sendButton).toHaveAttribute("aria-label", "Send message");
    await input.press("Enter");
    await expect(input).toHaveValue("");
    expect(compactRequests).toBe(0);
  });
});
