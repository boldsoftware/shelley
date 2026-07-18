import { test, expect } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

test.describe("Scroll behavior", () => {
  test("shows scroll-to-bottom button when scrolled up, auto-scrolls when at bottom", async ({
    page,
    request,
  }) => {
    // Seed a conversation with enough content via the API so we don't race
    // with other tests on the shared server (page.goto('/') used to pick up
    // whichever conversation was most recent, often mid-stream).
    const slug = await createConversationViaAPI(request, "echo message 0");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const input = page.locator('[data-testid="message-input"]');
    const sendButton = page.locator('[data-testid="send-button"]');
    await expect(input).toBeVisible({ timeout: 30000 });

    // Add more messages to ensure we have scrollable content.
    for (let i = 1; i < 4; i++) {
      await input.fill(`echo message ${i}`);
      await sendButton.click();
      // Wait for the agent reply for this specific message to appear.
      await expect(page.locator(`text=echo message ${i}`).last()).toBeVisible({ timeout: 30000 });
      await expect(page.getByTestId("agent-thinking")).toBeHidden({ timeout: 30000 });
    }

    // Get the messages container
    const messagesContainer = page.locator(".messages-container");
    const scrollButton = page.locator(".scroll-to-bottom-button");

    // The TOC must not synchronously measure every message during scroll.
    // That forces Safari to lay out content-visibility:auto messages that are
    // far off screen and turns one scroll event into a long main-thread stall.
    await page.evaluate(() => {
      const state = window as Window & {
        __messageRectReads?: number;
        __messagesScrollHeightReads?: number;
      };
      const originalRect = Element.prototype.getBoundingClientRect;
      const scrollHeight = Object.getOwnPropertyDescriptor(Element.prototype, "scrollHeight");
      const scrollTop = Object.getOwnPropertyDescriptor(Element.prototype, "scrollTop");
      if (!scrollHeight?.get) throw new Error("Element.scrollHeight getter not found");
      if (!scrollTop?.get || !scrollTop.set) throw new Error("Element.scrollTop accessors not found");
      state.__messageRectReads = 0;
      state.__messagesScrollHeightReads = 0;
      Element.prototype.getBoundingClientRect = function () {
        if (this instanceof HTMLElement && this.hasAttribute("data-message-id")) {
          state.__messageRectReads = (state.__messageRectReads || 0) + 1;
        }
        return originalRect.call(this);
      };
      Object.defineProperty(Element.prototype, "scrollHeight", {
        configurable: scrollHeight.configurable,
        enumerable: scrollHeight.enumerable,
        get() {
          if (this instanceof HTMLElement && this.classList.contains("messages-container")) {
            state.__messagesScrollHeightReads = (state.__messagesScrollHeightReads || 0) + 1;
          }
          return scrollHeight.get!.call(this);
        },
      });
      Object.defineProperty(Element.prototype, "scrollTop", {
        configurable: scrollTop.configurable,
        enumerable: scrollTop.enumerable,
        get() {
          return scrollTop.get!.call(this);
        },
        set(value: number) {
          // WebKit can wrap extremely large scroll offsets back to zero.
          scrollTop.set!.call(this, value > 0x7fffffff ? 0 : value);
        },
      });
    });

    // Scroll up to the top and verify the scroll-to-bottom button appears.
    //
    // Setting scrollTop dispatches the 'scroll' event asynchronously, so the
    // component's userScrolled flag isn't set synchronously. Under CI load a
    // late streaming delta can fire the ResizeObserver before that scroll
    // event lands and auto-scroll us back to the bottom, hiding the button for
    // good. Re-scroll inside a poll so such a yank-back can't permanently fail
    // the test, then assert the button stays visible once it's settled.
    await expect(async () => {
      await page.evaluate(() => {
        const state = window as Window & {
          __messageRectReads?: number;
          __messagesScrollHeightReads?: number;
        };
        state.__messageRectReads = 0;
        state.__messagesScrollHeightReads = 0;
      });
      await messagesContainer.evaluate((el) => {
        el.scrollTop = 0;
      });
      await expect(scrollButton).toBeVisible({ timeout: 1000 });
    }).toPass({ timeout: 30000 });
    await expect
      .poll(() =>
        page.evaluate(
          () => (window as Window & { __messageRectReads?: number }).__messageRectReads || 0,
        ),
      )
      .toBe(0);
    await expect
      .poll(() =>
        page.evaluate(
          () =>
            (window as Window & { __messagesScrollHeightReads?: number })
              .__messagesScrollHeightReads || 0,
        ),
      )
      .toBe(0);

    await page.locator(".toc-button").click();
    await expect(page.locator(".toc-entry-top")).toHaveClass(/toc-entry-active/);

    // The TOC's bottom action must stay pinned while lazy content expands.
    await messagesContainer.evaluate((container) => {
      const list = container.querySelector(".messages-list");
      const sentinel = container.querySelector(".messages-bottom-sentinel");
      if (!list || !sentinel) throw new Error("message list sentinel not found");
      const spacer = document.createElement("div");
      spacer.dataset.testid = "lazy-bottom-growth";
      list.insertBefore(spacer, sentinel);
      let height = 0;
      const grow = () => {
        height += 400;
        spacer.style.height = `${height}px`;
        if (height < 1200) requestAnimationFrame(grow);
      };
      requestAnimationFrame(grow);
    });
    await page.locator(".toc-entry-bottom").click();
    await expect
      .poll(() =>
        messagesContainer.evaluate(
          (el) => Math.abs(el.scrollHeight - el.clientHeight - el.scrollTop) <= 1,
        ),
      )
      .toBe(true);

    await expect(async () => {
      await messagesContainer.evaluate((el) => {
        el.scrollTop = 0;
      });
      await expect(scrollButton).toBeVisible({ timeout: 1000 });
    }).toPass({ timeout: 30000 });

    // Click the button to return to the bottom. A late streaming-driven
    // auto-scroll may beat us to it and hide the button first; that's fine —
    // either path leaves us pinned at the bottom, which is what we're after.
    if (await scrollButton.isVisible()) {
      await scrollButton.click().catch(() => {});
    }

    // Button should disappear once we're back at bottom
    await expect(scrollButton).not.toBeVisible({ timeout: 5000 });

    // An upward wheel gesture must immediately release lazy-layout pinning.
    await messagesContainer.evaluate((el) => {
      el.dispatchEvent(new WheelEvent("wheel", { deltaY: -200, bubbles: true }));
      el.scrollTop = 0;
    });
    await expect(scrollButton).toBeVisible({ timeout: 5000 });
    await messagesContainer.evaluate((container) => {
      const button = document.querySelector<HTMLButtonElement>(".scroll-to-bottom-button");
      const list = container.querySelector(".messages-list");
      const sentinel = container.querySelector(".messages-bottom-sentinel");
      if (!button || !list || !sentinel) throw new Error("scroll controls not found");
      button.click();
      const spacer = document.createElement("div");
      spacer.style.height = "1200px";
      list.insertBefore(spacer, sentinel);
      container.dispatchEvent(new WheelEvent("wheel", { deltaY: -200, bubbles: true }));
      container.scrollTop = Math.max(0, container.scrollTop - 200);
    });
    await expect
      .poll(() =>
        messagesContainer.evaluate(
          (el) => el.scrollHeight - el.clientHeight - el.scrollTop > 100,
        ),
      )
      .toBe(true);
    await expect(scrollButton).toBeVisible({ timeout: 5000 });
    await scrollButton.click();
    await expect
      .poll(() =>
        messagesContainer.evaluate(
          (el) => Math.abs(el.scrollHeight - el.clientHeight - el.scrollTop) <= 1,
        ),
      )
      .toBe(true);

    // Send another message - should auto-scroll since we're at bottom
    await input.fill("echo final message");
    await sendButton.click();

    // Wait for the user message to appear (predictable is fast, so don't
    // race on the transient agent-thinking indicator).
    await expect(page.locator("text=echo final message").last()).toBeVisible({ timeout: 30000 });

    // Button should not appear since we're following the conversation
    await expect(scrollButton).not.toBeVisible({ timeout: 5000 });
  });
});
