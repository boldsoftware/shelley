import { expect, test, type Page } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

const portrait = { width: 393, height: 851 };
const keyboardOpen = { width: 393, height: 420 };
const landscape = { width: 851, height: 360 };

test.use({ viewport: portrait, isMobile: true, hasTouch: true });

// Headless mobile emulation does not open Android's real software keyboard.
// Assert the browser's keyboard-resize policy separately, then exercise the
// layout at the resulting reduced size. A viewport resize alone would pass
// without fixing the Android bug, because it resizes BOTH viewports already.
test("the app opts into keyboard-driven layout viewport resizing", async ({ page }) => {
  await page.goto("/new");
  const viewport = page.locator('meta[name="viewport"]');
  await expect(viewport).toHaveCount(1);
  await expect(viewport).toHaveAttribute(
    "content",
    /(?:^|,)\s*interactive-widget\s*=\s*resizes-content\s*(?:,|$)/,
  );
});

async function expectComposerWithinViewport(page: Page) {
  await expect(async () => {
    const viewport = await page.evaluate(() => ({
      top: window.visualViewport!.offsetTop,
      bottom: window.visualViewport!.offsetTop + window.visualViewport!.height,
    }));
    for (const testId of ["message-input", "send-button", "attach-button"]) {
      const control = page.getByTestId(testId);
      await expect(control).toBeVisible();
      const box = await control.boundingBox();
      expect(box, `${testId} must have a bounding box`).not.toBeNull();
      expect(box!.y, `${testId} must not be above the visible viewport`).toBeGreaterThanOrEqual(
        viewport.top - 1,
      );
      expect(
        box!.y + box!.height,
        `${testId} must not be below the visible viewport`,
      ).toBeLessThanOrEqual(viewport.bottom + 1);
    }
  }).toPass();
}

for (const context of ["new conversation", "existing conversation"] as const) {
  test(`${context}: typing stays visible through keyboard-sized resizes`, async ({
    page,
    request,
  }) => {
    const url =
      context === "new conversation"
        ? "/new"
        : `/c/${await createConversationViaAPI(
            request,
            "echo " +
              Array.from({ length: 30 }, (_, i) => `Conversation paragraph ${i}.`).join("\n\n"),
          )}`;
    await page.goto(url);
    const input = page.getByTestId("message-input");
    await expect(input).toBeEnabled();
    await input.fill("Typing with the keyboard open");
    await expect(input).toBeFocused();

    await page.setViewportSize(keyboardOpen);
    await expectComposerWithinViewport(page);

    const message = Array.from({ length: 12 }, (_, i) => `Draft line ${i + 1}`).join("\n");
    await input.fill(message);
    await expectComposerWithinViewport(page);
    await expect(input).toHaveValue(message);
    await expect(input).toBeFocused();

    await page.setViewportSize(landscape);
    await expectComposerWithinViewport(page);
    await expect(input).toHaveValue(message);

    // Closing the keyboard restores space without losing the draft or focus.
    await page.setViewportSize(portrait);
    await expectComposerWithinViewport(page);
    await expect(input).toHaveValue(message);
    await expect(input).toBeFocused();

    await page.getByTestId("send-button").click();
    await expect(input).toHaveValue("");
  });
}
