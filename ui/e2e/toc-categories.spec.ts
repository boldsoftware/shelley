import { test, expect } from "@playwright/test";
import { createConversationViaAPI, makePNG } from "./helpers";

for (const [colorScheme, userColor, replyColor] of [
  ["light", "rgb(126, 34, 206)", "rgb(0, 68, 103)"],
  ["dark", "rgb(192, 132, 252)", "rgb(56, 189, 248)"],
] as const) {
  test(`Jump menu preserves source colors (${colorScheme})`, async ({ page, request }) => {
    await page.emulateMedia({ colorScheme });
    const image = `data:image/png;base64,${makePNG(16, 16).toString("base64")}`;
    const slug = await createConversationViaAPI(
      request,
      `markdown: ![TOC image](${image})\n\n${"TOC reply.\n\n".repeat(30)}`,
    );
    await page.goto(`/c/${slug}`);
    await expect(page.locator(".message-agent img")).toBeVisible();
    await page.locator(".toc-button").click();
    const menu = page.locator(".toc-popover");
    for (const [kind, label, icon, color] of [
      ["user", "User prompt", "•", userColor],
      ["eot", "Model reply", "✓", replyColor],
    ] as const) {
      const row = menu.locator(`.toc-entry-${kind}`);
      await expect(row).toHaveAccessibleName(new RegExp(`^${label}:`));
      await expect(row.locator(".toc-entry-icon")).toHaveText(icon);
      await expect(row.locator(".toc-entry-label")).toHaveCSS("color", color);
      await expect(row.locator(".toc-entry-icon")).toHaveCSS("color", color);
    }
    const reply = menu.locator(".toc-entry-eot");
    await reply.hover();
    for (const selected of [false, true]) {
      if (selected) {
        await reply.click();
        await page.locator(".toc-button").click();
        await expect(reply).toHaveClass(/toc-entry-active/);
      }
      await expect(reply.locator(".toc-entry-label")).toHaveCSS("color", replyColor);
      await expect(reply.locator(".toc-entry-icon")).toHaveCSS("color", replyColor);
      await expect(reply.locator(".toc-entry-thumbnail")).toHaveCSS("border-top-color", replyColor);
    }
  });
}
