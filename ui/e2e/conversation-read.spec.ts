import { expect, test } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

test("opening a chat clears New across browsers and keeps the open row quiet", async ({
  page,
  browser,
  baseURL,
  request,
}) => {
  const { conversationId } = await createConversationViaAPIWithDetails(
    request,
    "echo: first visit",
  );
  const observer = await browser.newPage({ baseURL, viewport: { width: 1280, height: 900 } });
  const row = `[data-conversation-id="${conversationId}"]`;
  const badge = observer.locator(row).getByTestId("conversation-new-badge");
  try {
    await observer.goto("/new");
    await expect(observer.locator(row)).toBeVisible();
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.goto("/new");
    await expect(page.locator(row)).toBeVisible();

    const reply = await request.post(`/api/conversation/${conversationId}/chat`, {
      data: { message: "echo: ready for review", model: "predictable" },
    });
    expect(reply.ok()).toBeTruthy();
    await expect(badge).toBeVisible();
    await expect(badge).toHaveCSS("font-size", "10px");
    await expect(badge).toHaveCSS("font-weight", "500");
    await expect(badge).toHaveCSS("line-height", "14px");
    await expect(badge).toHaveCSS("margin-right", "2px");
    await expect(badge).toHaveCSS("vertical-align", "1px");
    await expect(page.locator(row).getByTestId("conversation-new-badge")).toBeVisible();
    await expect(
      page.locator(row).locator(".conversation-preview").getByTestId("conversation-new-badge"),
    ).toBeVisible();
    await expect(
      page
        .locator(row)
        .locator(".drawer-conversation-header-row")
        .getByTestId("conversation-new-badge"),
    ).toHaveCount(0);

    // Keep the receipt in flight until we've inspected both drawers.
    let release!: () => void;
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    const readPath = `/api/conversation/${conversationId}/read`;
    await page.route(
      `**${readPath}`,
      async (route) => {
        await held;
        await route.continue();
      },
      { times: 1 },
    );
    try {
      await page.bringToFront();
      await Promise.all([
        page.waitForRequest((r) => new URL(r.url()).pathname === readPath),
        page.locator(row).click(),
      ]);
      await expect(page.locator(row)).toHaveAttribute("aria-current", "page");
      await expect(page.locator(row).getByTestId("conversation-new-badge")).toHaveCount(0);
      await expect(badge).toBeVisible();
    } finally {
      release();
    }
    await expect(badge).toHaveCount(0);
    const other = await createConversationViaAPIWithDetails(request, "echo: another unread chat");
    await observer.reload();
    await expect(
      observer
        .locator(`[data-conversation-id="${other.conversationId}"]`)
        .getByTestId("conversation-new-badge"),
    ).toBeVisible();
    await expect(observer.locator(row)).toBeVisible();
    await expect(badge).toHaveCount(0);
  } finally {
    await observer.close();
  }
});
