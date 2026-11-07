import { test, expect } from "@playwright/test";
import type { ConversationWithState } from "../src/types";
import { createConversationViaAPIWithDetails, fillConversationQuery } from "./helpers";

test.use({ timezoneId: "America/New_York" });

test("Daily grouping expands subagents last touched on other days", async ({ page, request }) => {
  await page.clock.setFixedTime(new Date("2026-10-08T16:00:00Z"));
  const { conversationId, slug } = await createConversationViaAPIWithDetails(
    request,
    "echo: parent ready",
  );
  const corpus = (await (
    await request.get("/api/conversations")
  ).json()) as ConversationWithState[];
  const original = corpus.find((c) => c.conversation_id === conversationId);
  expect(original).toBeTruthy();
  const parent = { ...original!, updated_at: "2026-10-08T16:00:00Z", subagent_count: 8 };
  const children = Array.from({ length: 8 }, (_, index) => ({
    ...parent,
    conversation_id: `older-subagent-${index}`,
    slug: `older-subagent-${index}`,
    parent_conversation_id: conversationId,
    user_initiated: false,
    updated_at: index % 2 ? "2026-10-07T16:00:00Z" : "2026-10-06T16:00:00Z",
    subagent_count: 0,
    working: false,
    agent_working: false,
    running_background_jobs: 0,
  }));
  await page.route("**/api/conversations/snapshot", (route) =>
    route.fulfill({ json: { conversations: [parent, ...children], hash: "cross-day-subagents" } }),
  );
  await page.route("**/api/stream2*", (route) => route.abort());
  await page.goto(`/c/${slug}`);
  await page.getByRole("button", { name: "Open conversations", exact: true }).click();
  await page.getByRole("button", { name: "Group conversations" }).click();
  await page
    .locator(".group-by-menu-item")
    .filter({ hasText: /^Daily$/ })
    .click();
  await expect(page.getByTestId("conversation-day-filter").locator(".drawer-day-count")).toHaveText(
    "1",
  );
  const row = page.locator(`[data-conversation-id="${conversationId}"]`);
  await expect(row.locator(".subagent-count-badge")).toHaveText("8");
  await row.getByRole("button", { name: "Expand subagents", exact: true }).click();
  await expect(page.locator(".drawer-subagent-list .subagent-item")).toHaveCount(8);
  expect(
    (await page.locator(".drawer-subagent-list .conversation-title").allTextContents())
      .map((title) => title.trim())
      .sort(),
  ).toEqual(children.map((child) => child.slug).sort());
});

test("Daily returns to today only after sending in an older conversation", async ({
  page,
  request,
}) => {
  await page.clock.setFixedTime(new Date("2026-10-08T16:00:00Z"));
  const older = await createConversationViaAPIWithDetails(request, "day send older");
  const today = await createConversationViaAPIWithDetails(request, "day send today");
  const response = await request.get("/api/conversations");
  expect(response.ok()).toBeTruthy();
  const corpus = (await response.json()) as ConversationWithState[];
  const fixtures = [
    [older.conversationId, "2026-10-07T16:00:00Z"],
    [today.conversationId, "2026-10-08T16:00:00Z"],
  ].map(([id, updated_at]) => {
    const conversation = corpus.find((c) => c.conversation_id === id);
    expect(conversation).toBeTruthy();
    return { ...conversation!, updated_at };
  });
  await page.route("**/api/conversations", (route) => route.fulfill({ json: fixtures }));
  await page.route("**/api/conversations/snapshot", (route) =>
    route.fulfill({ json: { conversations: fixtures, hash: "before-send" } }),
  );
  let releaseListUpdate!: () => void;
  const listUpdate = new Promise<void>((resolve) => {
    releaseListUpdate = resolve;
  });
  await page.route("**/api/stream2*", async (route) => {
    await listUpdate;
    await route.fulfill({
      contentType: "text/event-stream",
      body: `data: ${JSON.stringify({
        conversation_list_patch: {
          reset: true,
          new_hash: "after-send",
          at: "2026-10-08T16:00:00Z",
          patch: [
            {
              op: "replace",
              path: "",
              value: fixtures.map((c) => ({ ...c, updated_at: "2026-10-08T16:00:00Z" })),
            },
          ],
        },
      })}\n\n`,
    });
  });
  await page.goto(`/c/${today.slug}`);
  const openDrawer = page.getByRole("button", { name: "Open conversations", exact: true });
  await openDrawer.click();
  await page.getByRole("button", { name: "Group conversations" }).click();
  await page
    .locator(".group-by-menu-item")
    .filter({ hasText: /^Daily$/ })
    .click();
  const picker = page.getByTestId("conversation-day-filter");
  const date = picker.locator(".drawer-day-date");
  const count = picker.locator(".drawer-day-count");
  const olderRow = page.locator(
    `.conversation-item[data-conversation-id="${older.conversationId}"]`,
  );
  await picker.getByRole("button", { name: "Previous day" }).click();
  await expect(date).toHaveText("Yesterday");
  await olderRow.locator(".conversation-title").click();
  await expect(page).toHaveURL(new RegExp(`/c/${older.slug}$`));
  await openDrawer.click();
  await expect(date).toHaveText("Yesterday");
  await expect(olderRow).toBeVisible();
  await page.getByRole("button", { name: "Close conversations", exact: true }).click();

  const chatPath = `**/api/conversation/${older.conversationId}/chat`;
  await page.route(chatPath, (route) =>
    route.fulfill({ status: 500, contentType: "text/plain", body: "daily send failed" }),
  );
  const input = page.getByTestId("message-input");
  const send = page.getByTestId("send-button");
  await input.fill("Hello");
  const failed = page.waitForResponse((r) =>
    r.url().endsWith(`/conversation/${older.conversationId}/chat`),
  );
  await send.click();
  expect((await failed).status()).toBe(500);
  await expect(input).toHaveValue("Hello");
  await openDrawer.click();
  await expect(date).toHaveText("Yesterday");
  await expect(olderRow).toBeVisible();
  await page.getByRole("button", { name: "Close conversations", exact: true }).click();

  await page.unroute(chatPath);
  const accepted = page.waitForResponse((r) =>
    r.url().endsWith(`/conversation/${older.conversationId}/chat`),
  );
  await send.click();
  expect((await accepted).ok()).toBeTruthy();
  await openDrawer.click();
  await expect(date).toHaveText("Today");
  await expect(count).toHaveText("1");
  await expect(olderRow).toBeHidden();
  releaseListUpdate();
  await expect(olderRow).toBeVisible();
  await expect(olderRow).toHaveClass(/\bactive\b/);
  await expect(count).toHaveText("2");
});

test("Daily grouping pages and counts last-touched chats and selects calendar dates", async ({
  page,
  request,
}) => {
  await page.clock.setFixedTime(new Date("2026-10-08T16:00:00Z"));
  const today = await createConversationViaAPIWithDetails(request, "day paging today");
  const alsoToday = await createConversationViaAPIWithDetails(request, "day paging today too");
  const yesterday = await createConversationViaAPIWithDetails(request, "day paging yesterday");
  const earlier = await createConversationViaAPIWithDetails(request, "day paging earlier");
  const response = await request.get("/api/conversations");
  expect(response.ok()).toBeTruthy();
  const corpus = (await response.json()) as ConversationWithState[];
  const fixtures = [
    [today.conversationId, "2026-10-08T04:00:00Z", "day-today"],
    [alsoToday.conversationId, "2026-10-08T17:00:00Z", "day-today"],
    [yesterday.conversationId, "2026-10-08T03:59:59Z", "day-yesterday"],
    [earlier.conversationId, "2026-10-06T16:00:00Z", "day-earlier"],
  ].map(([id, updated_at, tag]) => {
    const conversation = corpus.find((c) => c.conversation_id === id);
    expect(conversation).toBeTruthy();
    return {
      ...conversation!,
      created_at: "2025-01-01T00:00:00Z",
      updated_at,
      tags: JSON.stringify([tag]),
    };
  });
  await page.route("**/api/conversations", (route) => route.fulfill({ json: fixtures }));
  await page.route("**/api/conversations/snapshot", (route) =>
    route.fulfill({ json: { conversations: fixtures, hash: "day-filter-fixture" } }),
  );
  await page.route("**/api/conversations/search?*", (route) => route.fulfill({ json: fixtures }));
  await page.route("**/api/conversations/archived", (route) =>
    route.fulfill({ json: fixtures.map((c) => ({ ...c, archived: true })) }),
  );
  // The stream stays open and quiet: a failing one would put "Reconnecting…"
  // above the search, whose inset this measures.
  await page.route("**/api/stream2*", () => {});
  await page.goto("/");
  await page.getByRole("button", { name: "Open conversations", exact: true }).click();
  const picker = page.getByTestId("conversation-day-filter");
  const row = (id: string) => page.locator(`.conversation-item[data-conversation-id="${id}"]`);
  const previous = picker.getByRole("button", { name: "Previous day" });
  const next = picker.getByRole("button", { name: "Next day" });
  const date = picker.locator(".drawer-day-date");
  const count = picker.locator(".drawer-day-count");
  const groupBy = async (label: string) => {
    await page.getByRole("button", { name: "Group conversations" }).click();
    await expect(page.locator(".group-by-menu-item").nth(3)).toHaveText("Tags");
    await expect(page.locator(".group-by-menu-item").nth(4)).toHaveText("Daily");
    await page
      .locator(".group-by-menu-item")
      .filter({ hasText: new RegExp(`^${label}$`) })
      .click();
  };
  await expect(picker).toBeHidden();
  await page.getByRole("button", { name: "Search conversations..." }).click();
  const searchShell = page.locator(".drawer-search-shell");
  await expect(searchShell).toBeVisible();
  const searchInset = await searchShell.evaluate((el) => {
    const header = el.closest(".drawer")!.querySelector(".drawer-header")!;
    return el.getBoundingClientRect().top - header.getBoundingClientRect().bottom;
  });
  expect(searchInset).toBe(8);
  await expect(picker).toBeHidden();
  await groupBy("Daily");
  await expect(date).toHaveText("Today");
  await expect(count).toHaveText("2");
  await expect(row(today.conversationId)).toBeVisible();
  await expect(row(yesterday.conversationId)).toBeHidden();
  const listGap = await page
    .locator(".conversation-list .conversation-item:visible")
    .first()
    .evaluate((el) => {
      const search = el.closest(".drawer")!.querySelector(".drawer-search")!;
      return el.getBoundingClientRect().top - search.getBoundingClientRect().bottom;
    });
  expect(listGap).toBe(4);
  await expect(next).toBeDisabled();
  await previous.click();
  await expect(date).toHaveText("Yesterday");
  await expect(count).toHaveText("1");
  await expect(row(yesterday.conversationId)).toBeVisible();
  await expect(row(today.conversationId)).toBeHidden();

  await page.getByRole("button", { name: "Add tag filter" }).click();
  await expect(
    page.locator('[data-testid="tag-filter-option"][data-tag="day-yesterday"]'),
  ).toBeVisible();
  await expect(page.locator('[data-testid="tag-filter-option"][data-tag="day-today"]')).toHaveCount(
    0,
  );
  await page.locator('[data-testid="tag-filter-option"][data-tag="day-yesterday"]').click();
  await next.click();
  await expect(count).toHaveText("0");
  await page.getByRole("button", { name: "Clear search", exact: true }).click();
  await expect(count).toHaveText("2");

  await groupBy("Directory");
  await expect(picker).toBeHidden();
  await expect(row(today.conversationId)).toBeVisible();
  await expect(row(yesterday.conversationId)).toBeVisible();
  await groupBy("Daily");
  const searchResponse = page.waitForResponse("**/api/conversations/search?*");
  await fillConversationQuery(page.locator(".drawer-search-input"), "day paging");
  await searchResponse;
  await expect(count).toHaveText("2");
  await previous.click();
  await expect(count).toHaveText("1");
  await expect(row(yesterday.conversationId)).toBeVisible();
  await page.getByRole("button", { name: "Clear search", exact: true }).click();
  await page.getByRole("button", { name: "View Archived" }).click();
  await expect(count).toHaveText("1");
  await expect(row(yesterday.conversationId)).toBeVisible();
  await expect(row(today.conversationId)).toBeHidden();
  await page.getByRole("button", { name: "Back to conversations" }).click();

  await picker.locator(".drawer-day-label").click();
  const calendar = page.getByRole("dialog", { name: "Choose last-touched day" });
  await expect(calendar).toBeVisible();
  const day = (n: number) =>
    calendar.locator(
      `[data-pc-section="daycell"][aria-label="${n}"]:not([data-p-other-month="true"]) [data-pc-section="day"]`,
    );
  await expect(day(8).locator(".drawer-calendar-count")).toHaveText("2");
  await expect(day(7).locator(".drawer-calendar-count")).toHaveText("1");
  await expect(day(6).locator(".drawer-calendar-count")).toHaveText("1");
  await expect(day(5).locator(".drawer-calendar-count")).toHaveCount(0);
  await expect(calendar.locator(".drawer-calendar-circle")).toHaveCount(0);
  expect(await day(7).evaluate((el) => getComputedStyle(el).backgroundColor)).not.toBe(
    "rgba(0, 0, 0, 0)",
  );
  await expect(day(9)).toHaveAttribute("aria-disabled", "true");
  await day(6).click();
  await expect(calendar).toBeHidden();
  await expect(date).toHaveText("Tue, Oct 6");
  await expect(count).toHaveText("1");
  await expect(row(earlier.conversationId)).toBeVisible();
  expect(await picker.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBeTruthy();
  await previous.click();
  await expect(count).toHaveText("0");
  await expect(page.getByTestId("day-filter-empty")).toBeVisible();
  await expect(next).toBeEnabled();
  await picker.locator(".drawer-day-label").click();
  await expect(calendar).toBeVisible();
  await expect(day(8).locator(".drawer-calendar-count")).toHaveText("2");
  await expect(day(7).locator(".drawer-calendar-count")).toHaveText("1");
  await calendar.getByRole("button", { name: "Today", exact: true }).click();
  await expect(calendar).toBeHidden();
  await expect(date).toHaveText("Today");
  await expect(count).toHaveText("2");
  await previous.click();
  await picker.getByRole("button", { name: "Today", exact: true }).click();
  await expect(date).toHaveText("Today");
  await expect(count).toHaveText("2");
  await expect(picker.getByRole("button", { name: "Today", exact: true })).toBeHidden();
  await page.getByRole("button", { name: "Search conversations..." }).click();
  await expect(picker).toBeVisible();
  await picker.getByRole("button", { name: "Turn off Daily" }).click();
  await expect(picker).toBeHidden();
  await expect(row(today.conversationId)).toBeVisible();
  await expect(row(yesterday.conversationId)).toBeVisible();
  await groupBy("Daily");
  await expect(date).toHaveText("Today");
  await expect(count).toHaveText("2");
  await page.reload();
  await page.getByRole("button", { name: "Open conversations", exact: true }).click();
  await expect(date).toHaveText("Today");
  await expect(count).toHaveText("2");
  await page.setViewportSize({ width: 1280, height: 800 });
  expect(await picker.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBeTruthy();
});
