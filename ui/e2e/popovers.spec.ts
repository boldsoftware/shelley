import { test, expect } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

// Anchored-popover contract for the two floating popups migrating to PrimeVue
// Popover: the ConversationTOC "Jump to…" panel and the ContextUsageBar token
// popup. These specs pin the DOM/ARIA contract (classes, labels, dismissal
// behavior) so it holds across the hand-rolled and PrimeVue implementations.

test.describe("Conversation TOC popover", () => {
  test("opens from the nav button, lists entries, and dismisses", async ({ page, request }) => {
    test.setTimeout(60000);
    const slug = await createConversationViaAPI(request, "echo table of contents");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const tocButton = page.locator(".toc-button");
    await expect(tocButton).toBeVisible({ timeout: 30000 });
    await expect(tocButton).toHaveAttribute("aria-expanded", "false");
    await expect(tocButton).toHaveAccessibleName("Conversation table of contents");

    await tocButton.click();
    const popover = page.locator(".toc-popover");
    await expect(popover).toBeVisible();
    await expect(popover.locator(".p-virtualscroller")).toBeVisible();
    await expect(tocButton).toHaveAttribute("aria-expanded", "true");
    await expect(popover.locator(".toc-popover-title")).toHaveText("Jump to…");

    // First/last entries are the fixed top/bottom anchors; the seeded user
    // message appears as a .toc-entry-user row in between.
    const entries = popover.locator(".toc-entry");
    await expect(entries.first()).toContainText("Top of conversation");
    await expect(entries.last()).toContainText("End of conversation");
    await expect(popover.locator(".toc-entry-user").first()).toContainText(
      "echo table of contents",
    );

    // Escape dismisses.
    await page.keyboard.press("Escape");
    await expect(popover).toBeHidden();
    await expect(tocButton).toHaveAttribute("aria-expanded", "false");

    // Outside click dismisses.
    await tocButton.click();
    await expect(popover).toBeVisible();
    await page.locator(".messages-container").click({ position: { x: 10, y: 10 } });
    await expect(popover).toBeHidden();

    // Clicking a user entry closes the popover and records a #m-<short> hash.
    await tocButton.click();
    await expect(popover).toBeVisible();
    await popover.locator(".toc-entry-user").first().click();
    await expect(popover).toBeHidden();
    await expect(async () => {
      expect(new URL(page.url()).hash).toMatch(/^#m-[a-zA-Z0-9]+$/);
    }).toPass({ timeout: 5000 });
  });
});

test.describe("Message action bar", () => {
  test("uses CSS-only hover labels", async ({ page, request }) => {
    const slug = await createConversationViaAPI(request, "echo action bar");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const message = page
      .locator('[data-testid="message"]')
      .filter({ hasText: "echo action bar" })
      .first();
    await expect(message).toBeVisible({ timeout: 30000 });
    await message.hover();

    const copy = message.getByRole("button", { name: "Copy" });
    await expect(copy).toBeVisible();
    await expect(copy).toHaveAttribute("data-tooltip", "Copy");
  });
});

test.describe("Context usage popup", () => {
  test("toggles from the usage bar and closes on outside click", async ({ page, request }) => {
    test.setTimeout(60000);
    const slug = await createConversationViaAPI(request, "echo context usage");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const bar = page.locator(".context-usage-bar");
    await expect(bar).toBeVisible({ timeout: 30000 });

    await bar.click();
    const popup = page.locator(".chat-context-popup");
    await expect(popup).toBeVisible();
    await expect(popup).toContainText("tokens used");

    // Clicking the bar again toggles it closed.
    await bar.click();
    await expect(popup).toBeHidden();

    // Reopen, then an outside click dismisses.
    await bar.click();
    await expect(popup).toBeVisible();
    await page.locator(".messages-container").click({ position: { x: 10, y: 10 } });
    await expect(popup).toBeHidden();

    // Escape dismisses too (new with the PrimeVue Popover port).
    await bar.click();
    await expect(popup).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(popup).toBeHidden();
  });
});
