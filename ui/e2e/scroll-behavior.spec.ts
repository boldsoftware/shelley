import { test, expect } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

test.describe("Scroll behavior", () => {
  test("shows scroll-to-bottom button when scrolled up, auto-scrolls when at bottom", async ({
    page,
    request,
  }) => {
    // Several sequential agent round-trips below; give headroom over the
    // default 30s so this doesn't time out under full-suite load.
    test.setTimeout(120_000);
    // Seed a conversation with enough content via the API so we don't race
    // with other tests on the shared server (page.goto('/') used to pick up
    // whichever conversation was most recent, often mid-stream).
    const slug = await createConversationViaAPI(request, "echo message 0");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const input = page.locator('[data-testid="message-input"]');
    const sendButton = page.locator('[data-testid="send-button"]');
    await expect(input).toBeVisible({ timeout: 30000 });

    // Add more messages to ensure we have scrollable content. The shadcn
    // redesign is more vertically compact than the old UI, so a few short echo
    // exchanges no longer overflow the viewport; seed enough to guarantee the
    // messages container actually scrolls.
    for (let i = 1; i < 6; i++) {
      await input.fill(`echo message ${i}`);
      await sendButton.click();
      // Wait for the agent reply for this specific message to appear.
      await expect(page.locator(`text=echo message ${i}`).last()).toBeVisible({ timeout: 30000 });
      await expect(page.getByTestId("agent-thinking")).toBeHidden({ timeout: 30000 });
    }

    // Get the messages container
    const messagesContainer = page.locator(".messages-container");
    const scrollButton = page.locator(".scroll-to-bottom-button");

    // Scroll up to the top and verify the scroll-to-bottom button appears.
    //
    // Setting scrollTop dispatches the 'scroll' event asynchronously, so the
    // component's userScrolled flag isn't set synchronously. Under CI load a
    // late streaming delta can fire the ResizeObserver before that scroll
    // event lands and auto-scroll us back to the bottom, hiding the button for
    // good. Re-scroll inside a poll so such a yank-back can't permanently fail
    // the test, then assert the button stays visible once it's settled.
    await expect(async () => {
      await messagesContainer.evaluate((el) => {
        el.scrollTop = 0;
      });
      await expect(scrollButton).toBeVisible({ timeout: 1000 });
    }).toPass({ timeout: 30000 });

    // Click the button to return to the bottom. A late streaming-driven
    // auto-scroll may beat us to it and hide the button first; that's fine —
    // either path leaves us pinned at the bottom, which is what we're after.
    if (await scrollButton.isVisible()) {
      await scrollButton.click().catch(() => {});
    }

    // Button should disappear once we're back at bottom
    await expect(scrollButton).not.toBeVisible({ timeout: 5000 });

    // Send another message - should auto-scroll since we're at bottom
    await input.fill("echo final message");
    await sendButton.click();

    // Wait for the user message to appear (predictable is fast, so don't
    // race on the transient agent-thinking indicator).
    await expect(page.locator("text=echo final message").last()).toBeVisible({ timeout: 30000 });

    // Button should not appear since we're following the conversation
    await expect(scrollButton).not.toBeVisible({ timeout: 5000 });
  });
});
