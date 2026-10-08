import { expect, test } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

test.use({ viewport: { width: 1280, height: 800 }, isMobile: false, hasTouch: false });

// Compact in Place is offered even in a conversation started without the
// compact_in_place tool: the button enables it (without the context nudges),
// marks that in the log, and then asks the agent to compact.
test.describe("Compact in Place without the tool", () => {
  test("enables the tool, then asks the agent", async ({ page, request }) => {
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "echo: compact in place later",
    );
    await page.goto(`/c/${slug}`);
    await page.locator(".context-usage-label").click();
    await page.getByTestId("compact-in-place-button").click();

    await expect(page.getByTestId("message-modelchange")).toHaveAttribute(
      "aria-label",
      "Enabled the compact_in_place tool.",
    );
    await expect(page.getByTestId("message-modelchange")).toContainText("+compact_in_place");
    await expect(page.getByText("Compact your context in place.")).toBeVisible();

    const body = await (await request.get(`/api/conversation/${conversationId}`)).json();
    const opts = JSON.parse(body.conversation.conversation_options);
    expect(opts.tool_overrides.compact_in_place).toBe("on");
    expect(opts.disable_compact_nudges).toBe(true);
  });

  test("waits for the turn to end", async ({ page, request }) => {
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "echo: busy",
    );
    const chat = await request.post(`/api/conversation/${conversationId}/chat`, {
      data: { message: "bash: sleep 30", model: "predictable" },
    });
    expect(chat.ok()).toBeTruthy();
    await page.goto(`/c/${slug}`);
    await page.locator(".context-usage-label").click();
    const button = page.getByTestId("compact-in-place-button");
    await expect(button).toBeDisabled();
    await expect(
      page.getByText("Finish or stop the current turn to compact in place."),
    ).toBeVisible();
    await request.post(`/api/conversation/${conversationId}/cancel`);
    await expect(button).toBeEnabled();
  });
});
