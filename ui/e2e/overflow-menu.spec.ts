import { test, expect } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

// The top-right overflow ("kebab") menu is built from PrimeVue Popover/Select
// plus compact native icon buttons. See components/ChatOverflowMenu.vue. The
// DOM contract (.chat-overflow-menu-wrapper / .btn-icon / .overflow-menu-item)
// is covered by other specs (agents-md-vim, diff-viewer-find); here we
// exercise the PrimeVue-specific controls.
test.describe("Overflow menu (PrimeVue)", () => {
  test("popover opens, compact controls and language Select work", async ({ page, request }) => {
    test.setTimeout(60000);
    await page.addInitScript(() => {
      Object.defineProperty(window, "Notification", {
        configurable: true,
        value: class FakeNotification {
          static permission = "default";
          static requestPermission = async () => {
            FakeNotification.permission = "denied";
            return "denied";
          };
          close() {}
        },
      });
    });

    const slug = await createConversationViaAPI(request, "Hello");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    // Reset persisted prefs so assertions are deterministic regardless of
    // what an earlier test in the same worker stored.
    await page.evaluate(() => {
      localStorage.setItem("shelley-theme", "system");
      localStorage.setItem(
        "shelley-notification-prefs",
        JSON.stringify({ channels: { favicon: { enabled: true }, browser: { enabled: true } } }),
      );
    });
    await page.reload();
    await page.waitForLoadState("domcontentloaded");

    // Open the PrimeVue Popover.
    const trigger = page.locator(".chat-overflow-menu-wrapper .btn-icon");
    await expect(trigger).toBeVisible({ timeout: 10000 });
    await trigger.click();

    const popover = page.locator(".chat-overflow-popover");
    await expect(popover).toBeVisible();

    // --- Compact controls: every choice is visible; the active icon is emphasized ---
    await expect(popover.locator(".overflow-quick-control")).toHaveCount(3);
    await expect(popover.getByText("Brevity", { exact: true })).toBeVisible();
    await expect(popover.getByText("Look", { exact: true })).toBeVisible();
    await expect(popover.getByText("Notifications", { exact: true })).toBeVisible();
    await expect(popover.locator(".overflow-choice-options")).toHaveCount(3);
    await expect(popover.locator(".overflow-choice-option")).toHaveCount(7);
    await expect(popover.locator(".overflow-choice-option.is-selected")).toHaveCount(3);

    const themeOptions = popover.getByTestId("theme-cycle");
    await expect(themeOptions.getByRole("button", { name: "System" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );

    const notificationOptions = popover.getByTestId("notification-toggle");
    const notificationsOff = notificationOptions.getByRole("button", {
      name: "Disable Notifications",
    });
    const notificationsOn = notificationOptions.getByRole("button", {
      name: "Enable Notifications",
    });
    await expect(notificationsOn).toHaveAttribute("aria-pressed", "true");
    await notificationsOff.click();
    await expect(notificationsOff).toHaveAttribute("aria-pressed", "true");
    await expect(notificationsOn).toBeEnabled();
    await notificationsOn.click();
    const notificationsBlocked = notificationOptions.getByRole("button", {
      name: "Blocked by browser",
    });
    await expect(notificationsBlocked).toBeDisabled();
    expect(
      await page.evaluate(
        () =>
          JSON.parse(localStorage.getItem("shelley-notification-prefs") || "{}").channels?.browser
            ?.enabled,
      ),
    ).toBe(false);

    // Select Light, then Dark directly.
    await themeOptions.getByRole("button", { name: "Light" }).click();
    await expect(page.locator("html")).not.toHaveClass(/dark/);
    expect(await page.evaluate(() => localStorage.getItem("shelley-theme"))).toBe("light");

    await themeOptions.getByRole("button", { name: "Dark" }).click();
    await expect(page.locator("html")).toHaveClass(/dark/);
    expect(await page.evaluate(() => localStorage.getItem("shelley-theme"))).toBe("dark");

    // --- Language Select: open and pick Japanese ---
    const select = popover.locator(".overflow-language-select");
    await select.click();
    // The overlay renders inside the popover (appendTo="self"), so the popover
    // must stay open while we pick.
    const jpOption = page.locator(".p-select-option").filter({ hasText: /日本語/ });
    await expect(jpOption).toBeVisible();
    await jpOption.click();
    expect(await page.evaluate(() => localStorage.getItem("shelley-locale"))).toBe("ja");
    await expect(popover).toBeVisible();

    // The compact control labels re-translate live while the menu stays open.
    await expect(themeOptions.getByRole("button", { name: "ダーク" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );

    // Reset locale so we don't leak Japanese UI into sibling tests' assertions.
    await page.evaluate(() => localStorage.setItem("shelley-locale", "en"));
  });

  test("can show only user, end-of-turn, and notification messages", async ({ page, request }) => {
    test.setTimeout(60000);

    const response = await request.post("/debug/loremipsum?json=1", {
      form: { size: "18", model: "predictable" },
    });
    expect(response.ok()).toBeTruthy();
    const { conversation_id: conversationId } = await response.json();

    await page.goto(`/c/${conversationId}`);
    await expect(page.getByText("Turn 1:", { exact: false }).first()).toBeVisible();
    await expect(page.getByText("I'll work on turn 1.", { exact: false }).first()).toBeVisible();
    await expect(page.getByText("Done with turn 1.", { exact: false }).first()).toBeVisible();
    await expect(page.locator('[data-testid="tool-call-completed"]').first()).toBeVisible();

    await page.locator(".chat-overflow-menu-wrapper .btn-icon").click();
    const viewOptions = page.getByTestId("conversation-view-toggle");
    const seeAll = viewOptions.getByRole("button", { name: "See All" });
    const seeEndOfTurn = viewOptions.getByRole("button", {
      name: "See End of Turn Messages Only",
    });
    await expect(seeAll).toHaveAttribute("aria-pressed", "true");
    await seeEndOfTurn.click();
    await expect(seeEndOfTurn).toHaveAttribute("aria-pressed", "true");

    await expect(page.getByText("Turn 1:", { exact: false }).first()).toBeVisible();
    await expect(page.getByText("Done with turn 1.", { exact: false }).first()).toBeVisible();
    await expect(page.getByText("Warning on turn 18:", { exact: false })).toBeVisible();
    await expect(page.getByText("I'll work on turn 1.", { exact: false })).toHaveCount(0);
    await expect(page.locator('[data-testid="tool-call-completed"]')).toHaveCount(0);
    expect(await page.evaluate(() => localStorage.getItem("shelley-conversation-view"))).toBe(
      "end-of-turn",
    );

    await page.reload();
    await expect(page.getByText("I'll work on turn 1.", { exact: false })).toHaveCount(0);
    await expect(page.getByText("Done with turn 1.", { exact: false }).first()).toBeVisible();
  });
});
