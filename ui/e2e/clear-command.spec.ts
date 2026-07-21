import { test, expect } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

// /clear starts a fresh generation in the same conversation: it drops the
// prior context and re-hydrates a vanilla system prompt (like compaction, but
// without the summary). The UI marks the boundary with a generation divider
// and the older messages are retained but no longer sent to the LLM.
test.describe("/clear slash command", () => {
  test("clears context and starts a new generation", async ({ page, request }) => {
    test.setTimeout(60000);

    const slug = await createConversationViaAPI(request, "echo: before clear");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const messageInput = page.getByTestId("message-input");
    await expect(messageInput).toBeVisible({ timeout: 30000 });

    // No generation divider yet.
    await expect(page.locator(".generation-divider")).toHaveCount(0);

    // Run /clear. It sends immediately (no args) and starts a new generation.
    await messageInput.fill("/clear");
    const sendButton = page.getByTestId("send-button");
    await expect(sendButton).toBeEnabled({ timeout: 5000 });
    await sendButton.click();

    // A generation divider should appear, and the composer should clear.
    await expect(page.locator(".generation-divider")).toHaveCount(1, { timeout: 30000 });
    await expect(messageInput).toHaveValue("");

    // The prior message text is retained above the divider.
    await expect(page.getByText("before clear").first()).toBeVisible();
  });

  test("appears in the slash-command menu", async ({ page, request }) => {
    const slug = await createConversationViaAPI(request, "echo: menu seed");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const messageInput = page.getByTestId("message-input");
    await expect(messageInput).toBeVisible({ timeout: 30000 });

    await messageInput.fill("/cle");
    const menu = page.getByTestId("slash-command-menu");
    await expect(menu).toBeVisible({ timeout: 5000 });
    await expect(menu.getByText("/clear", { exact: true })).toBeVisible();
  });
});
