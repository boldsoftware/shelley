import { expect, test, type APIRequestContext } from "@playwright/test";
import { createConversationViaAPIWithDetails, fillConversationQuery } from "./helpers";

async function markRead(request: APIRequestContext, conversationId: string): Promise<void> {
  const snapshot = await request.get("/api/conversations/snapshot");
  expect(snapshot.ok()).toBeTruthy();
  const { conversations } = await snapshot.json();
  const conversation = conversations.find(
    (c: { conversation_id: string }) => c.conversation_id === conversationId,
  );
  expect(conversation).toBeTruthy();
  const response = await request.post(`/api/conversation/${conversationId}/read`, {
    data: { read_at: conversation.last_attention_at },
  });
  expect(response.ok()).toBeTruthy();
}

test("unread filter toggles, combines with text and tags, and updates on read", async ({
  page,
  request,
}) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto("/new");
  await expect(page.getByTestId("message-input")).toBeVisible();
  const unread = await createConversationViaAPIWithDetails(request, "echo: unread filter needle");
  const read = await createConversationViaAPIWithDetails(request, "echo: unread filter needle");
  await markRead(request, read.conversationId);
  const tag = `unread-${unread.conversationId}`;
  for (const id of [unread.conversationId, read.conversationId]) {
    expect(
      (await request.post(`/api/conversation/${id}/tags`, { data: { tags: [tag] } })).ok(),
    ).toBeTruthy();
  }
  await page.reload();
  const unreadRow = page.locator(
    `.conversation-item[data-conversation-id="${unread.conversationId}"]`,
  );
  const readRow = page.locator(`.conversation-item[data-conversation-id="${read.conversationId}"]`);
  await expect(unreadRow).toHaveClass(/conversation-item-unread/);
  await expect(readRow).toBeVisible();
  await page.getByRole("button", { name: "Search conversations...", exact: true }).click();
  const query = page.locator(".drawer-search-input");
  const toggle = page.getByTestId("unread-toggle");
  await expect(toggle).toHaveText("unread");
  await expect(toggle).toHaveAttribute("aria-pressed", "false");

  const picker = page.getByTestId("tag-filter-panel");
  await page.getByRole("button", { name: "Add tag filter" }).click();
  await expect(picker).toBeVisible();
  await toggle.click();
  await expect(picker).toBeVisible();
  await expect(unreadRow).toBeVisible();
  await expect(readRow).toBeHidden();
  for (const remove of ["button", "pill"]) {
    await fillConversationQuery(query, "is:unread ");
    await expect(picker).toBeHidden();
    await page.getByRole("button", { name: "Add tag filter" }).click();
    await expect(picker).toBeVisible();
    await expect(readRow).toBeHidden();
    if (remove === "button") await toggle.click();
    else await query.getByRole("button", { name: "Remove is:unread", exact: true }).click();
    await expect(picker).toBeVisible();
    await expect(toggle).toHaveAttribute("aria-pressed", "false");
    await expect(readRow).toBeVisible();
  }
  await fillConversationQuery(query, "");
  await toggle.click();
  await expect(query).toHaveAttribute("data-query-value", "is:unread ");
  await expect(toggle).toHaveAttribute("aria-pressed", "true");
  await expect(unreadRow).toBeVisible();
  await expect(readRow).toBeHidden();

  await fillConversationQuery(query, `is:unread tag:${tag} needle`);
  await expect(unreadRow).toBeVisible();
  await expect(readRow).toBeHidden();
  await toggle.click();
  await expect(query).toHaveAttribute("data-query-value", `tag:${tag} needle `);
  await expect(readRow).toBeVisible();
  await toggle.click();
  await expect(readRow).toBeHidden();
  await markRead(request, unread.conversationId);
  await expect(unreadRow).toBeHidden();
  await query
    .locator('[data-conversation-query-token="unread"] .conversation-query-token-delete')
    .click();
  await expect(toggle).toHaveAttribute("aria-pressed", "false");
  await expect(unreadRow).toBeVisible();
  await expect(readRow).toBeVisible();
});

test("the unread filter also keeps read chats with foreground, background, or delegated work", async ({
  page,
  request,
}) => {
  const { conversationId } = await createConversationViaAPIWithDetails(
    request,
    "echo: working filter",
  );
  let activity = "foreground";
  await page.route("**/api/stream2*", (route) => route.abort());
  await page.route("**/api/conversations/snapshot", async (route) => {
    const response = await route.fetch();
    const snapshot = await response.json();
    const parent = snapshot.conversations.find(
      (c: { conversation_id: string }) => c.conversation_id === conversationId,
    );
    expect(parent).toBeTruthy();
    Object.assign(parent, {
      last_attention_at: 0,
      working: activity === "foreground",
      running_background_jobs: activity === "background" ? 1 : 0,
    });
    if (activity === "delegated") {
      snapshot.conversations.push({
        ...parent,
        conversation_id: "working-filter-child",
        parent_conversation_id: conversationId,
        user_initiated: false,
        working: true,
      });
    }
    await route.fulfill({ response, json: snapshot });
  });
  await page.setViewportSize({ width: 1280, height: 900 });
  for (activity of ["foreground", "background", "delegated", "idle"]) {
    await page.goto("/new");
    await page.getByRole("button", { name: "Search conversations...", exact: true }).click();
    await page.getByTestId("unread-toggle").click();
    const row = page.locator(`.conversation-item[data-conversation-id="${conversationId}"]`);
    if (activity === "idle") {
      await expect(row).toBeHidden();
      await expect(page.getByTestId("unread-filter-empty")).toBeVisible();
    } else {
      await expect(row).toBeVisible();
      await expect(row).not.toHaveClass(/conversation-item-unread/);
    }
  }
});
