import { test, expect } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

// Issue #260: mobile users have no keyboard shortcut for the Cmd/Ctrl+K
// command palette, so the top-right overflow ("…") menu exposes a
// "Command menu" item (its first entry) that opens it. The Playwright project
// runs the Pixel 5 mobile viewport, matching the mobile use case.
test.describe("Command palette from overflow menu", () => {
  test("overflow menu item opens the command palette", async ({ page, request }) => {
    test.setTimeout(60000);

    const slug = await createConversationViaAPI(request, "Hello");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    // The palette starts closed.
    await expect(page.locator(".command-palette-input")).toHaveCount(0);

    // Open the overflow ("…") menu.
    await page.getByRole("button", { name: "More options" }).click();

    // The first item is the command menu opener.
    const item = page.locator(".overflow-menu-item").first();
    await expect(item).toBeVisible();
    await expect(item).toHaveText(/Command menu/);
    // The row advertises its Cmd/Ctrl+K shortcut, like the other menu rows.
    await expect(item.locator(".overflow-menu-shortcut")).toBeVisible();
    await item.tap();

    // The palette opens and behaves like the keyboard-opened one.
    const search = page.locator(".command-palette-input");
    await expect(search).toBeVisible();
    await search.fill("notification");
    await expect(page.locator(".command-palette-item").first()).toBeVisible();

    // Escape closes it again.
    await page.keyboard.press("Escape");
    await expect(search).toHaveCount(0);
  });
});
