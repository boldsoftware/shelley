import { expect, test, type Locator, type Page } from "@playwright/test";
import type { ConversationWithState } from "../src/types";
import { createConversationViaAPIWithDetails } from "./helpers";

test.use({ reducedMotion: "reduce" });

async function openDrawer(page: Page) {
  await expect(page.locator(".header")).toBeVisible();
  const open = page.getByRole("button", { name: "Open conversations", exact: true });
  if (await open.isVisible()) await open.click();
  await expect(page.locator(".drawer")).toBeVisible();
}

async function expectCentered(circle: Locator, line: Locator) {
  await expect(circle).toBeVisible();
  // Geometry catches the old macOS offset despite the header's align-items:center.
  await expect(async () => {
    const dot = await circle.boundingBox();
    const box = await line.boundingBox();
    expect(Math.abs(dot!.y + dot!.height / 2 - box!.y - box!.height / 2)).toBeLessThanOrEqual(1);
  }).toPass({ timeout: 5000 });
}

for (const theme of ["light", "dark"] as const) {
  test(`conversation card activity and selection (${theme})`, async ({ page, request }) => {
    await page.addInitScript((value) => {
      localStorage.setItem("shelley-theme", value);
      localStorage.setItem("shelley-group-by", "none");
    }, theme);
    const { conversationId } = await createConversationViaAPIWithDetails(request, "echo: activity");
    const response = await request.get("/api/conversations/snapshot");
    expect(response.ok()).toBeTruthy();
    const snapshot: { conversations: ConversationWithState[] } = await response.json();
    const parent = snapshot.conversations.find((row) => row.conversation_id === conversationId)!;
    const child = {
      ...parent,
      conversation_id: "activity-child",
      slug: "activity-child",
      parent_conversation_id: parent.conversation_id,
      user_initiated: false,
      subagent_count: 0,
    };
    const conversations = [parent, child];
    // Stub list state only; the real parent provides chat content and navigation.
    await page.route("**/api/conversations/snapshot", (route) =>
      route.fulfill({ json: { conversations, hash: "activity-cards" } }),
    );
    await page.route("**/api/conversations/search**", (route) =>
      route.fulfill({ json: conversations }),
    );
    await page.route("**/api/stream2**", (route) => route.abort());
    const card = page.locator(`[data-conversation-id="${conversationId}"]`);
    const badge = card.locator(".subagent-count-badge");
    for (const [name, working, jobs, subWorking, subJobs] of [
      ["idle", false, 0, false, 0],
      ["parent working", true, 0, false, 0],
      ["subagent working", false, 0, true, 0],
      ["both working", true, 0, true, 0],
      ["parent background job", false, 1, false, 0],
      ["subagent background job", false, 0, false, 1],
    ] as const) {
      await test.step(name, async () => {
        parent.working = parent.agent_working = working;
        parent.running_background_jobs = jobs;
        child.working = child.agent_working = subWorking;
        child.running_background_jobs = subJobs;
        const subRunning = subWorking || subJobs > 0;
        const parentRunning = working || jobs > 0 || subRunning;
        await page.goto("/new");
        await openDrawer(page);
        await page.mouse.move(0, 0);
        await expect(card).not.toHaveClass(/\bactive\b/);
        // Rows stay clear; activity is shown by dots and badges only.
        await expect(card).toHaveCSS("background-color", "rgba(0, 0, 0, 0)");
        // Selection is solid primary regardless of activity.
        await card.locator(".conversation-title").click();
        await expect(page).toHaveURL(new RegExp(`/c/${parent.slug}$`));
        await openDrawer(page);
        await page.mouse.move(0, 0);
        await expect(card).toHaveClass(/\bactive\b/);
        const primary = await card.evaluate((row) => {
          const probe = document.createElement("div");
          probe.style.backgroundColor = "var(--primary)";
          row.parentElement!.appendChild(probe);
          const color = getComputedStyle(probe).backgroundColor;
          probe.remove();
          return color;
        });
        await expect(card).toHaveCSS("background-color", primary);
        const parentCircle = card.locator(".drawer-working-indicator");
        await expect(parentCircle).toHaveCount(parentRunning ? 1 : 0);
        if (parentRunning) {
          await expectCentered(parentCircle, card.locator(".drawer-conversation-header-row"));
        }
        await expect(badge).toHaveText(subRunning ? "1/1" : "1");
        const childCircle = badge.getByTestId("subagent-badge-running");
        await expect(childCircle).toHaveCount(subRunning ? 1 : 0);
        if (subRunning) await expectCentered(childCircle, badge);

        await badge.click();
        const childCard = page.locator(".drawer-subagent-list .conversation-item");
        await expect(childCard).toBeVisible();
        await expect(childCard).toHaveCSS("background-color", "rgba(0, 0, 0, 0)");
        await expect(childCard).not.toHaveClass(/\bactive\b/);
        const group = page.locator(".drawer-subagent-list");
        const parentBox = await card.boundingBox();
        const childBox = await childCard.boundingBox();
        expect(childBox!.y).toBeGreaterThan(parentBox!.y + parentBox!.height);
        await badge.click();
        await expect(group).toHaveCount(0);
      });
    }
  });
}
