import { expect, test, type Page } from "@playwright/test";
import type { ConversationWithState } from "../src/types";

test.use({
  viewport: { width: 1280, height: 720 },
  isMobile: false,
  hasTouch: false,
});

function conversation(id: string, isDraft = false): ConversationWithState {
  return {
    conversation_id: id,
    slug: isDraft ? null : id,
    user_initiated: true,
    created_at: "2026-07-23T12:00:00Z",
    updated_at: "2026-07-23T12:00:00Z",
    cwd: null,
    archived: false,
    parent_conversation_id: null,
    model: "predictable",
    conversation_options: "{}",
    current_generation: 1,
    agent_working: false,
    tags: "[]",
    is_draft: isDraft,
    draft: isDraft ? "unfinished message" : "",
    queued_messages: "[]",
    working: false,
    subagent_count: 0,
    preview: "Preview",
    max_sequence_id: 0,
  };
}

async function stubConversationList(page: Page, conversations: ConversationWithState[]) {
  await page.route("**/api/conversations/snapshot", (route) =>
    route.fulfill({ json: { conversations, hash: `test-${conversations.length}` } }),
  );
  await page.route("**/api/stream2**", (route) => route.abort());
}

test.describe("conversation drawer startup and app bar", () => {
  test("starts collapsed when the only item is a draft", async ({ page }) => {
    await stubConversationList(page, [conversation("draft", true)]);

    await page.goto("/new");

    await expect(page.locator(".drawer")).toHaveClass(/collapsed/);
    await expect(page.getByRole("button", { name: "Expand sidebar" })).toBeVisible();
  });

  test("starts expanded for multiple conversations with aligned app-bar titles", async ({
    page,
  }) => {
    await stubConversationList(page, [conversation("first"), conversation("second")]);

    await page.goto("/new");

    const drawer = page.locator(".drawer");
    await expect(drawer).not.toHaveClass(/collapsed/);
    await expect(page.locator(".drawer-title")).toBeVisible();

    const metrics = await page.evaluate(() => {
      function inspect(selector: string) {
        const element = document.querySelector<HTMLElement>(selector);
        if (!element) throw new Error(`Missing ${selector}`);
        const rect = element.getBoundingClientRect();
        const style = getComputedStyle(element);
        return {
          top: rect.top,
          bottom: rect.bottom,
          fontFamily: style.fontFamily,
          fontSize: style.fontSize,
          fontWeight: style.fontWeight,
          lineHeight: style.lineHeight,
          letterSpacing: style.letterSpacing,
          margin: style.margin,
        };
      }
      return {
        drawerHeader: inspect(".drawer-header"),
        chatHeader: inspect(".header"),
        drawerTitle: inspect(".drawer-title"),
        chatTitle: inspect(".header-title"),
      };
    });

    expect(metrics.drawerHeader.top).toBe(metrics.chatHeader.top);
    expect(metrics.drawerHeader.bottom).toBe(metrics.chatHeader.bottom);
    expect(metrics.drawerTitle.top).toBe(metrics.chatTitle.top);
    expect(metrics.drawerTitle.bottom).toBe(metrics.chatTitle.bottom);
    expect(metrics.drawerTitle.fontFamily).toBe(metrics.chatTitle.fontFamily);
    expect(metrics.drawerTitle.fontSize).toBe(metrics.chatTitle.fontSize);
    expect(metrics.drawerTitle.fontWeight).toBe(metrics.chatTitle.fontWeight);
    expect(metrics.drawerTitle.lineHeight).toBe(metrics.chatTitle.lineHeight);
    expect(metrics.drawerTitle.letterSpacing).toBe(metrics.chatTitle.letterSpacing);
    expect(metrics.drawerTitle.margin).toBe("0px");
    expect(metrics.chatTitle.margin).toBe("0px");
  });
});
