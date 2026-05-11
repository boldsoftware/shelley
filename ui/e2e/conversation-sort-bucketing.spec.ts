import { test, expect } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

const BUCKET_MS = 5 * 60 * 1000;

async function fetchUpdatedAt(
  request: import("@playwright/test").APIRequestContext,
  conversationId: string,
): Promise<string> {
  const resp = await request.get(`/api/conversation/${conversationId}`);
  expect(resp.ok()).toBeTruthy();
  const body = await resp.json();
  return body.conversation.updated_at as string;
}

function bucketOf(updatedAt: string): number {
  return Math.floor(new Date(updatedAt).getTime() / BUCKET_MS);
}

test.describe("Conversation drawer ordering", () => {
  test("does not flip-flop conversations updated within the same 5-minute bucket", async ({
    page,
    request,
  }) => {
    // Two conversations created back-to-back: B is created after A so its
    // ULID is newer and (with bucketed sort) it sorts first whenever both
    // share an updated_at bucket.
    const a = await createConversationViaAPIWithDetails(request, "first conversation");
    const b = await createConversationViaAPIWithDetails(request, "second conversation");

    await page.goto(`/c/${b.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

    await page.locator('button[aria-label="Open conversations"]').click();
    await expect(page.locator(".drawer.open")).toBeVisible();

    const initialOrder = await page
      .locator(".drawer .conversation-item .conversation-title")
      .allInnerTexts();
    expect(initialOrder).toContain(a.slug);
    expect(initialOrder).toContain(b.slug);

    // Send a message to A. Without bucketed sort, A's updated_at jumps past
    // B's and the drawer reorders. With bucketed sort, the change is
    // invisible as long as both fall in the same 5-minute bucket.
    const sendResp = await request.post(`/api/conversation/${a.conversationId}/chat`, {
      data: { message: "poke", model: "predictable" },
    });
    expect(
      sendResp.ok(),
      `chat failed: ${sendResp.status()} ${await sendResp.text()}`,
    ).toBeTruthy();
    await expect(async () => {
      const resp = await request.get(`/api/conversation/${a.conversationId}`);
      expect(resp.ok()).toBeTruthy();
      const body = await resp.json();
      const turns = (body.messages || []).filter(
        (m: { type: string; end_of_turn?: boolean }) => m.type === "agent" && m.end_of_turn,
      );
      expect(turns.length).toBeGreaterThanOrEqual(2);
    }).toPass({ timeout: 30000 });

    const finalOrder = await page
      .locator(".drawer .conversation-item .conversation-title")
      .allInnerTexts();

    // Avoid a wall-clock flake: only assert stability when A and B truly
    // share a bucket. If the test straddled a 5-minute boundary, fall back
    // to asserting bucket-derived order (newer bucket first).
    const aUpdated = await fetchUpdatedAt(request, a.conversationId);
    const bUpdated = await fetchUpdatedAt(request, b.conversationId);
    if (bucketOf(aUpdated) === bucketOf(bUpdated)) {
      expect(finalOrder.indexOf(a.slug)).toBe(initialOrder.indexOf(a.slug));
      expect(finalOrder.indexOf(b.slug)).toBe(initialOrder.indexOf(b.slug));
    } else {
      // Buckets diverged across a wall-clock boundary; the more recently
      // updated conversation must be listed first.
      const aFinal = finalOrder.indexOf(a.slug);
      const bFinal = finalOrder.indexOf(b.slug);
      if (bucketOf(aUpdated) > bucketOf(bUpdated)) {
        expect(aFinal).toBeLessThan(bFinal);
      } else {
        expect(bFinal).toBeLessThan(aFinal);
      }
    }
  });
});
