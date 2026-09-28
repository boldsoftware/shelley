import { test, expect } from "@playwright/test";

// Reproduce the user's ctrl+` reports with REAL trusted key events. Runs in
// the default chromium project and, with PW_FIREFOX=1, in real Firefox.
test.describe("ctrl+backtick terminal shortcut", () => {
  test("opens terminal from body and message input; toggles from shell", async ({
    page,
  }) => {
    await page.goto("/");
    await page.waitForLoadState("domcontentloaded");

    const panel = page.locator(".terminal-panel");
    await expect(panel).toHaveCount(1);
    // Desktop refocuses the input after minimizing; mobile deliberately skips
    // that (soft-keyboard guard in focusMessageInput()).
    const hasTouch = await page.evaluate(() => "ontouchstart" in window);

    // 1. Focus on the page body: should open the terminal.
    await page.keyboard.press("Control+`");
    await expect(panel).not.toHaveClass(/terminal-panel-minimized/);
    await expect(page.locator(".xterm")).toHaveCount(1);

    // 2. Shell focused: should minimize the panel and refocus the input.
    await page.keyboard.press("Control+`");
    await expect(panel).toHaveClass(/terminal-panel-minimized/);
    if (!hasTouch) {
      await expect(page.locator('[data-testid="message-input"]')).toBeFocused();
    }

    // 3. Message input focused: should re-expand and focus the shell.
    await page.keyboard.press("Control+`");
    await expect(panel).not.toHaveClass(/terminal-panel-minimized/);
    await expect(page.locator(".xterm-helper-textarea")).toBeFocused();
  });

  test("focuses a visible terminal when hydrated foreign terminals exist", async ({ page }) => {
    // Regression: toggleTerminal used to focus ephemeralTerminals[last],
    // which may be scoped to a DIFFERENT conversation and therefore not
    // rendered in this panel's tab list — an invisible no-op.
    await page.goto("/");
    await page.waitForLoadState("domcontentloaded");

    // Spawn a global terminal on the landing page (conversationId null).
    await page.keyboard.press("Control+`");
    await expect(page.locator(".xterm")).toHaveCount(1);

    // Conversation A: spawn a conversation-SCOPED terminal via the overflow
    // menu item (which always spawns, unlike the shortcut).
    const mk = (msg: string) =>
      page.request.post("/api/conversations/new", {
        data: { message: msg, model: "predictable" },
      });
    const mkResp = await mk("reply with just: ok");
    expect(mkResp.ok()).toBeTruthy();
    const { slug: slugA } = await mkResp.json();
    await page.goto(`/c/${slugA}`);
    await page.waitForLoadState("domcontentloaded");
    await page.getByRole("button", { name: "More options" }).click();
    await page.locator('button.overflow-menu-item:has-text("Terminal")').click();
    await expect(page.locator(".xterm")).toHaveCount(2);

    // Navigate to conversation B (fresh SPA load hydrates both terminals:
    // one global, one scoped to A — not visible in B's tab list).
    const bResp = await mk("reply with just: ok");
    expect(bResp.ok()).toBeTruthy();
    const { slug: slugB } = await bResp.json();
    await page.goto(`/c/${slugB}`);
    await page.waitForLoadState("domcontentloaded");
    await expect(page.locator(".terminal-panel")).toHaveCount(1);

    // Ctrl+` must do something visible here: focus the global terminal (the
    // only one in this panel's tab list) instead of silently targeting
    // A's invisible terminal.
    await page.keyboard.press("Control+`");
    await expect(page.locator(".xterm-helper-textarea").first()).toBeFocused();
    await expect(page.locator(".xterm")).toHaveCount(2); // no duplicate spawned
  });
});
