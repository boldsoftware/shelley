import { expect, test } from "@playwright/test";
import { setPageFeatureFlag } from "./helpers";

// While this Shelley archives to a Shelley archive store (server/archiver.go), a new
// conversation's composer has a Temporary toggle beside Send, whose tooltip
// says who can read the conversation. Chosen before the first message, it
// marks the conversation in its header and the drawer, and is gone after.
test.describe("temporary conversations", () => {
  test.use({ viewport: { width: 1280, height: 800 }, isMobile: false, hasTouch: false });

  test("the toggle shows only while archiving", async ({ page }) => {
    await page.goto("/new");
    await expect(page.getByTestId("message-input")).toBeVisible();
    await expect(page.getByTestId("send-button")).toBeVisible();
    await expect(page.getByTestId("temporary-toggle")).toHaveCount(0);
  });

  test("a temporary conversation is chosen before its first message", async ({ page, request }) => {
    // The page's flag only: the store is never reached.
    await setPageFeatureFlag(page, "archiver", "https://archive-store.int.exe.xyz/");
    await page.goto("/new");
    const toggle = page.getByTestId("temporary-toggle");
    await expect(toggle).toBeVisible();
    await expect(toggle).toHaveAttribute("aria-pressed", "false");
    // Beside Send.
    const send = (await page.getByTestId("send-button").boundingBox())!;
    const box = (await toggle.boundingBox())!;
    expect(Math.abs(box.y + box.height / 2 - (send.y + send.height / 2))).toBeLessThan(4);
    expect(send.x - (box.x + box.width)).toBeLessThan(24);

    await toggle.hover();
    await expect(page.locator(".p-tooltip")).toContainText("anyone with access to the store can read it");
    await toggle.click();
    await expect(toggle).toHaveAttribute("aria-pressed", "true");
    await page.getByTestId("message-input").hover();
    await toggle.hover();
    await expect(page.locator(".p-tooltip")).toContainText("isn't archived");

    const input = page.getByTestId("message-input");
    await input.fill("echo: plans for the surprise party");
    await input.press("Enter");
    await expect(page).toHaveURL(/\/c\//);
    await expect(page.getByTestId("temporary-badge")).toBeVisible();
    await expect(page.getByTestId("temporary-toggle")).toHaveCount(0);
    const row = page.locator(".conversation-item.active");
    await expect(row.getByTestId("drawer-temporary-badge")).toBeVisible();

    const id = await page.evaluate(() => window.location.pathname);
    const conversations = await (await request.get("/api/conversations")).json();
    const conv = conversations.find(
      (c: { slug: string; conversation_options: string }) => id === `/c/${c.slug}`,
    );
    expect(JSON.parse(conv.conversation_options).temporary).toBe(true);
  });
});
