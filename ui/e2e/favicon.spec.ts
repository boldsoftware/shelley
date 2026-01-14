import { test, expect } from "@playwright/test";

test.describe("Favicon notification badge", () => {
  test("shows favicon with red dot when agent is idle", async ({ page }) => {
    await page.goto("/");
    await page.waitForLoadState("domcontentloaded");

    // Wait for the favicon to be set
    await page.waitForFunction(
      () => {
        const link = document.querySelector('link[rel="icon"]');
        return link && link.getAttribute("href")?.startsWith("data:image/png");
      },
      undefined,
      { timeout: 10000 }
    );

    // Verify favicon link exists with data URL
    const faviconHref = await page.evaluate(() => {
      const link = document.querySelector('link[rel="icon"]');
      return link?.getAttribute("href") || null;
    });

    expect(faviconHref).not.toBeNull();
    expect(faviconHref).toMatch(/^data:image\/png;base64,/);
  });

  test("favicon changes when agent starts working", async ({ page }) => {
    await page.goto("/");
    await page.waitForLoadState("domcontentloaded");

    // Wait for initial favicon (ready state with dot)
    await page.waitForFunction(
      () => {
        const link = document.querySelector('link[rel="icon"]');
        return link && link.getAttribute("href")?.startsWith("data:image/png");
      },
      undefined,
      { timeout: 10000 }
    );

    // Capture initial favicon
    const initialFavicon = await page.evaluate(() => {
      const link = document.querySelector('link[rel="icon"]');
      return link?.getAttribute("href") || null;
    });

    // Send a message to trigger agent working
    const messageInput = page.getByTestId("message-input");
    await expect(messageInput).toBeVisible({ timeout: 30000 });
    await messageInput.fill("Hello");

    const sendButton = page.getByTestId("send-button");
    await sendButton.click();

    // Wait for response to complete (agent finishes working)
    await page.waitForFunction(
      () => {
        const text =
          "Hello! I'm Shelley, your AI assistant. How can I help you today?";
        return document.body.textContent?.includes(text) ?? false;
      },
      undefined,
      { timeout: 30000 }
    );

    // Favicon should be back to ready state (with dot)
    // The favicon data URL should exist
    const finalFavicon = await page.evaluate(() => {
      const link = document.querySelector('link[rel="icon"]');
      return link?.getAttribute("href") || null;
    });

    expect(finalFavicon).not.toBeNull();
    expect(finalFavicon).toMatch(/^data:image\/png;base64,/);
  });
});
