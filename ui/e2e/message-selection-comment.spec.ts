import { expect, test, type Page } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

async function selectTextIn(page: Page, selector: string, needle: string): Promise<void> {
  await page.evaluate(
    ({ sel, text }) => {
      const root = document.querySelector(sel);
      if (!root) throw new Error(`missing ${sel}`);
      const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
      let node: Text | null = null;
      let idx = -1;
      while (walker.nextNode()) {
        const cur = walker.currentNode as Text;
        const found = cur.data.indexOf(text);
        if (found !== -1) {
          node = cur;
          idx = found;
          break;
        }
      }
      if (!node || idx === -1) throw new Error(`text ${JSON.stringify(text)} not found in ${sel}`);
      const range = document.createRange();
      range.setStart(node, idx);
      range.setEnd(node, idx + text.length);
      const selection = window.getSelection()!;
      selection.removeAllRanges();
      selection.addRange(range);
    },
    { sel: selector, text: needle },
  );
}

test.describe("Message selection comment toolbar", () => {
  test("tapping Comment on touch injects a blockquote into the composer", async ({
    page,
    request,
  }) => {
    const slug = await createConversationViaAPI(request, "echo: water the basil before noon");
    await page.goto(`/c/${slug}`);

    const assistantMsg = page.locator('[data-testid="message"][data-commentable="true"]').first();
    await expect(assistantMsg).toContainText("water the basil before noon");

    await selectTextIn(page, '[data-testid="message"][data-commentable="true"]', "water the basil");

    const commentBtn = page.getByTestId("message-selection-comment-btn");
    await expect(commentBtn).toBeVisible();
    await expect(commentBtn).toBeInViewport();

    // Drive a realistic multi-frame touch tap via CDP so any selectionchange +
    // requestAnimationFrame between touchStart and touchEnd executes just as it
    // does under a real finger on Android Chrome.
    const box = (await commentBtn.boundingBox())!;
    const cx = Math.round(box.x + box.width / 2);
    const cy = Math.round(box.y + box.height / 2);
    const cdp = await page.context().newCDPSession(page);
    page.once("close", () => {
      cdp.detach().catch(() => {});
    });
    await cdp.send("Input.dispatchTouchEvent", {
      type: "touchStart",
      touchPoints: [{ x: cx, y: cy, id: 0 }],
    });
    await page.evaluate(
      () =>
        new Promise<void>((resolve) =>
          requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
        ),
    );
    await cdp.send("Input.dispatchTouchEvent", {
      type: "touchEnd",
      touchPoints: [],
    });

    const input = page.getByTestId("message-input");
    await expect(input).toHaveValue("> water the basil\n\n");
    await expect(input).toBeFocused();
    await expect(commentBtn).toBeHidden();

    // Also verify Playwright's high-level tap() works and appends cleanly.
    await selectTextIn(page, '[data-testid="message"][data-commentable="true"]', "before noon");
    await expect(commentBtn).toBeVisible();
    await commentBtn.tap();
    await expect(input).toHaveValue("> water the basil\n\n> before noon\n\n");
    await expect(commentBtn).toBeHidden();
  });

  test("selecting user message text does not show the Comment button", async ({
    page,
    request,
  }) => {
    const slug = await createConversationViaAPI(request, "echo: reply text here");
    await page.goto(`/c/${slug}`);

    const userMsg = page.locator('[data-testid="message"]:not([data-commentable="true"])').first();
    await expect(userMsg).toContainText("echo: reply text here");

    await selectTextIn(
      page,
      '[data-testid="message"]:not([data-commentable="true"])',
      "reply text here",
    );
    // Give selectionchange + rAF a turn to run if it were going to.
    await page.evaluate(
      () =>
        new Promise<void>((resolve) =>
          requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
        ),
    );
    await expect(page.getByTestId("message-selection-comment-btn")).toBeHidden();
  });

  test.describe("desktop mouse", () => {
    test.use({ viewport: { width: 1280, height: 800 }, hasTouch: false, isMobile: false });

    test("clicking Comment injects a blockquote into the composer", async ({ page, request }) => {
      const slug = await createConversationViaAPI(request, "echo: prune the mint");
      await page.goto(`/c/${slug}`);

      const assistantMsg = page.locator('[data-testid="message"][data-commentable="true"]').first();
      await expect(assistantMsg).toContainText("prune the mint");

      await selectTextIn(
        page,
        '[data-testid="message"][data-commentable="true"]',
        "prune the mint",
      );

      const commentBtn = page.getByTestId("message-selection-comment-btn");
      await expect(commentBtn).toBeVisible();

      // Pressing down on the button and releasing outside it should not inject a
      // quote or wedge future selection updates.
      const box = (await commentBtn.boundingBox())!;
      await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
      await page.mouse.down();
      await page.mouse.move(box.x + box.width + 80, box.y + box.height + 80);
      await page.mouse.up();

      const input = page.getByTestId("message-input");
      await expect(input).toHaveValue("");
      await expect(commentBtn).toBeVisible();

      await commentBtn.click();
      await expect(input).toHaveValue("> prune the mint\n\n");
      await expect(input).toBeFocused();
      await expect(commentBtn).toBeHidden();
    });
  });
});
