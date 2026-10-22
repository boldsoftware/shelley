import { test, expect } from "@playwright/test";
import {
  createConversationViaAPI,
  createConversationViaAPIWithDetails,
  installServerLinks,
  openModelPicker,
  testWorkingDirectory,
} from "./helpers";

test("mobile New is a plain plus immediately left of Call", async ({ page, request }) => {
  const slug = await createConversationViaAPI(request, "echo header controls");
  for (const width of [320, 390, 1280]) {
    await page.setViewportSize({ width, height: 800 });
    await page.goto(`/c/${slug}`);
    const plus = page
      .locator(".header")
      .getByRole("button", { name: "New Conversation", exact: true });
    const call = page.getByTestId("live-call-header");
    await expect(plus).toBeVisible();
    await expect(call).toBeVisible();
    const plusBox = (await plus.boundingBox())!;
    const callBox = (await call.boundingBox())!;
    if (width < 768) {
      expect(plusBox.x + plusBox.width).toBeLessThanOrEqual(callBox.x);
      expect(callBox.x - plusBox.x - plusBox.width).toBeLessThan(8);
      await expect(plus).toHaveCSS("background-color", "rgba(0, 0, 0, 0)");
      await expect(plus.locator("svg")).toHaveCSS("width", "22px");
    } else {
      expect(callBox.x + callBox.width).toBeLessThanOrEqual(plusBox.x);
    }
  }
  await page.setViewportSize({ width: 390, height: 800 });
  await page
    .locator(".header")
    .getByRole("button", { name: "New Conversation", exact: true })
    .click();
  await expect(page).toHaveURL(/\/new$/);
});

test("mobile model row matches Directory and saves live reasoning settings", async ({
  page,
  request,
}) => {
  const { slug, conversationId } = await createConversationViaAPIWithDetails(
    request,
    "echo mobile model",
  );
  await page.setViewportSize({ width: 390, height: 800 });
  await page.goto(`/c/${slug}`);
  const model = await openModelPicker(page);
  await expect(model).not.toHaveClass(/p-disabled/);
  await expect(model.locator("svg.chat-menu-icon")).toBeVisible();
  const directory = page
    .locator(".chat-overflow-popover")
    .getByRole("button", { name: /^Directory\b/ });
  const modelBox = (await model.boundingBox())!;
  const directoryBox = (await directory.boundingBox())!;
  expect(modelBox.height).toBe(directoryBox.height);
  expect(modelBox.x).toBe(directoryBox.x);
  expect(modelBox.width).toBe(directoryBox.width);
  expect(modelBox.y + modelBox.height).toBeLessThanOrEqual(directoryBox.y);
  await page
    .locator(".model-picker-panel")
    .getByRole("radio", { name: "high", exact: true })
    .click();
  await expect
    .poll(async () => {
      const { conversation } = await (
        await request.get(`/api/conversation/${conversationId}`)
      ).json();
      return JSON.parse(conversation.conversation_options).thinking_level;
    })
    .toBe("high");
  await expect(page.locator(".model-picker-panel")).toBeVisible();
  await page.reload();
  await openModelPicker(page);
  await expect(
    page.locator(".model-picker-panel").getByRole("radio", { name: "high", exact: true }),
  ).toHaveAttribute("aria-checked", "true");
});

for (const width of [393, 1280]) {
  test(`menu follows the requested four groups at ${width}px`, async ({ page, request }) => {
    await page.setViewportSize({ width, height: 851 });
    await installServerLinks(page);
    const slug = await createConversationViaAPI(request, "Hello");
    await page.goto(`/c/${slug}`);
    await page.getByRole("button", { name: "More options", exact: true }).click();
    const panel = page.locator(".overflow-menu-panel");
    await expect(panel).toBeVisible();
    const rows = await panel.evaluate((el) =>
      Array.from(el.children)
        .filter((child) => child.checkVisibility())
        .map((child) => {
          if (child.classList.contains("overflow-menu-divider")) return "separator";
          if (child.classList.contains("overflow-quick-controls")) return "preferences";
          const row = child.cloneNode(true) as HTMLElement;
          row
            .querySelectorAll(
              ".overflow-menu-shortcut, .overflow-menu-cwd, .overflow-menu-language, .version-menu-dot",
            )
            .forEach((extra) => extra.remove());
          return row.textContent!.trim().replace(/\s+/g, " ");
        }),
    );
    expect(rows).toEqual([
      // Phones keep workspace tools here, beside the header's New and Call controls.
      ...(width < 768 ? ["Diffs", "Git Graph", "Terminal"] : []),
      "Command menu",
      "Manage on exe.dev",
      ...(width < 768 ? ["vm.example"] : []),
      "Documentation",
      "separator",
      "Edit File…",
      ...(width < 768 ? ["Model", "Directory"] : []),
      "Export Conversation",
      "Archive Conversation",
      "separator",
      "Edit User AGENTS.md",
      "MCP Servers",
      "separator",
      "preferences",
      "Change Language",
      "Check for New Version",
    ]);
    await expect(panel.locator(".overflow-menu-divider")).toHaveCount(3);
  });
}

// The top-right overflow ("kebab") menu uses a PrimeVue Popover, compact
// native icon buttons, and the shared Modal for language selection. The
// DOM contract (.chat-overflow-menu-wrapper / .btn-icon / .overflow-menu-item)
// is covered by other specs (agents-md-vim).
test.describe("Overflow menu (PrimeVue)", () => {
  test("directory item opens the picker and closes the popover on mobile", async ({
    page,
    request,
    isMobile,
  }) => {
    test.skip(!isMobile, "Mobile overflow menu (Pixel 5 in the chromium project)");
    const slug = await createConversationViaAPI(request, "Hello");
    const trigger = page.getByRole("button", { name: "More options" });
    const popover = page.locator(".chat-overflow-popover");
    const directory = popover.getByRole("button", { name: /^Directory\b/ });

    await page.goto("/new");
    await trigger.tap();
    await expect(popover).toBeVisible();
    await expect(directory).toBeVisible();

    await page.goto(`/c/${slug}`);

    const picker = page.locator(".modal.directory-picker-modal");
    await expect(picker).toBeHidden();
    await trigger.tap();

    await expect(popover).toBeVisible();
    await expect(directory).toBeVisible();
    await expect(popover.getByRole("button", { name: /^Command menu\b/ })).toBeVisible();
    await expect(popover.getByRole("button", { name: /^Terminal\b/ })).toBeVisible();
    await expect(directory.locator(".overflow-menu-cwd")).toHaveText(testWorkingDirectory());
    await expect(directory.locator(".overflow-menu-cwd")).toHaveAttribute(
      "title",
      testWorkingDirectory(),
    );
    await directory.tap();

    await expect(picker).toBeVisible();
    await expect(popover).toBeHidden();
    await expect(trigger).toHaveAttribute("aria-expanded", "false");

    await picker.getByRole("button", { name: "Cancel", exact: true }).tap();
    await expect(picker).toBeHidden();
    await trigger.tap();
    await expect(directory).toBeVisible();
    await page.setViewportSize({ width: 1280, height: 800 });
    await expect(popover).toBeVisible();
    await expect(directory).toHaveCount(0);
  });

  test("popover opens and compact controls work", async ({ page, request }) => {
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

    await expect(popover).toBeVisible();
  });

  for (const viewport of [
    { width: 320, height: 568 },
    { width: 393, height: 851 },
    { width: 1280, height: 800 },
  ]) {
    test(`language action opens a compact picker at ${viewport.width}px`, async ({ page }) => {
      await page.setViewportSize(viewport);
      await page.goto("/new");
      const trigger = page.locator(".chat-overflow-menu-wrapper .btn-icon");
      const popover = page.locator(".chat-overflow-popover");
      const dialog = page
        .getByRole("dialog")
        .filter({ has: page.locator(".language-picker-modal") });
      await trigger.click();

      const action = popover.getByRole("button", { name: /Change Language/ });
      await expect(action).toHaveClass("overflow-menu-item");
      await expect(action).toContainText("English");
      await expect(action.getByRole("combobox")).toHaveCount(0);
      const rowHeight = await action.evaluate((el) => el.getBoundingClientRect().height);
      expect(rowHeight).toBeLessThanOrEqual(48);
      await action.click();

      await expect(popover).toBeHidden();
      await expect(dialog).toHaveAccessibleName("Change Language");
      await expect(dialog.getByRole("button", { pressed: true })).toHaveAccessibleName("English");
      const options = dialog
        .getByRole("group", { name: "Language", exact: true })
        .getByRole("button");
      await expect(options).toHaveCount(9);
      await options.last().scrollIntoViewIfNeeded();
      await expect(options.last()).toBeInViewport();
      const bounds = await dialog.boundingBox();
      expect(bounds).not.toBeNull();
      expect(bounds!.x).toBeGreaterThanOrEqual(0);
      expect(bounds!.y).toBeGreaterThanOrEqual(0);
      expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(viewport.width);
      expect(bounds!.y + bounds!.height).toBeLessThanOrEqual(viewport.height);

      await dialog.getByRole("button", { name: "日本語", exact: true }).click();
      await expect(dialog).toBeHidden();
      await expect(trigger).toBeFocused();
      expect(await page.evaluate(() => localStorage.getItem("shelley-locale"))).toBe("ja");
      await trigger.click();
      await expect(popover.getByText("外観", { exact: true })).toBeVisible();
      await popover.getByRole("button", { name: /言語を切り替える/ }).click();
      await expect(dialog.getByRole("button", { pressed: true })).toHaveAccessibleName("日本語");
      await dialog.getByRole("button", { name: "English", exact: true }).click();
      await page.reload();
      await trigger.click();
      await expect(popover.getByRole("button", { name: /Change Language/ })).toContainText(
        "English",
      );
    });
  }

  test("language picker dismisses without changes and supports keyboard selection", async ({
    page,
  }) => {
    await page.goto("/new");
    const trigger = page.locator(".chat-overflow-menu-wrapper .btn-icon");
    const popover = page.locator(".chat-overflow-popover");
    const dialog = page.getByRole("dialog").filter({ has: page.locator(".language-picker-modal") });
    const openPicker = async () => {
      await trigger.click();
      const action = popover.getByRole("button", { name: /Change Language/ });
      await action.focus();
      await page.keyboard.press("Enter");
      await expect(dialog).toBeVisible();
      await expect(dialog.getByRole("button", { name: "English", exact: true })).toBeFocused();
    };

    await openPicker();
    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
    await expect(trigger).toBeFocused();
    await expect(popover).toBeHidden();

    await openPicker();
    await dialog.getByRole("button", { name: "Close modal" }).click();
    await expect(dialog).toBeHidden();
    await expect(trigger).toBeFocused();

    await openPicker();
    await page.locator(".modal-overlay").click({ position: { x: 2, y: 2 } });
    await expect(dialog).toBeHidden();
    await expect(trigger).toBeFocused();
    expect(await page.evaluate(() => localStorage.getItem("shelley-locale"))).toBeNull();

    await openPicker();
    await page.keyboard.press("Tab");
    const japanese = dialog.getByRole("button", { name: "日本語", exact: true });
    await expect(japanese).toBeFocused();
    await expect(japanese).toHaveCSS("outline-style", "solid");
    await expect(japanese).toHaveCSS("outline-width", "2px");
    await page.keyboard.press("Enter");
    await expect(dialog).toBeHidden();
    await expect(trigger).toBeFocused();
    expect(await page.evaluate(() => localStorage.getItem("shelley-locale"))).toBe("ja");
    await page.reload();
    await trigger.click();
    await popover.getByRole("button", { name: /言語を切り替える/ }).click();
    await expect(dialog.getByRole("button", { name: "日本語", exact: true })).toBeFocused();
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
