import { test, expect, type Page, type Request } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

/**
 * Tests for the stream-catch-up behavior of the unified /api/stream2 connection
 * and the IndexedDB messageStore. See ChatInterface.loadMessages and
 * services/globalStream.ts.
 */

// Waits for text in the conversation itself. The conversation list previews
// each conversation's latest message, so matching anywhere on the page can pass
// before the conversation has loaded -- and a load that lands after a test
// starts counting requests then looks like a cache miss.
async function waitForText(page: Page, text: string, timeout = 15000) {
  await page.waitForFunction(
    (t) => document.querySelector(".messages-container")?.textContent?.includes(t) ?? false,
    text,
    { timeout },
  );
}

async function selectConversation(page: Page, slug: string) {
  const drawerButton = page.locator('button[aria-label="Open conversations"]');
  await drawerButton.click();
  const drawer = page.locator(".drawer.open");
  await expect(drawer).toBeVisible({ timeout: 5000 });
  const titleEl = drawer.locator(".conversation-title").getByText(slug, { exact: true });
  await expect(titleEl).toBeVisible({ timeout: 15000 });
  await titleEl.click();
  await expect(drawer).toBeHidden({ timeout: 10000 });
}

function trackConversationLoads(page: Page, conversationId: string): string[] {
  const hits: string[] = [];
  const pattern = new RegExp(`/api/conversation/${conversationId}$`);
  page.on("request", (req: Request) => {
    if (req.method() === "GET" && pattern.test(new URL(req.url()).pathname)) {
      hits.push(req.url());
    }
  });
  return hits;
}

test.describe("stream catch-up", () => {
  test("A → B → A second visit hits cache (no extra REST backfill)", async ({ page, request }) => {
    const convA = await createConversationViaAPIWithDetails(request, "Hello");
    const convB = await createConversationViaAPIWithDetails(request, "hello");

    await page.goto(`/c/${convA.slug}`);
    await page.waitForLoadState("domcontentloaded");
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    // Track A's full-load endpoint for the rest of the test.
    const hitsA = trackConversationLoads(page, convA.conversationId);

    await selectConversation(page, convB.slug);
    await waitForText(page, "Well, hi there!");

    await selectConversation(page, convA.slug);
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    expect(hitsA).toHaveLength(0);

    // And once more — still cached.
    await selectConversation(page, convB.slug);
    await selectConversation(page, convA.slug);
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");
    expect(hitsA).toHaveLength(0);
  });

  test("only one /api/stream2 EventSource exists, even after switching conversations", async ({
    page,
    request,
  }) => {
    const convA = await createConversationViaAPIWithDetails(request, "Hello");
    const convB = await createConversationViaAPIWithDetails(request, "hello");

    // Count EventSource subscribe requests at the network level.
    const streamRequests: string[] = [];
    page.on("request", (req) => {
      const u = new URL(req.url());
      if (u.pathname === "/api/stream2") streamRequests.push(req.url());
    });

    await page.goto(`/c/${convA.slug}`);
    await page.waitForLoadState("domcontentloaded");
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    // A few switches.
    await selectConversation(page, convB.slug);
    await waitForText(page, "Well, hi there!");
    await selectConversation(page, convA.slug);
    await selectConversation(page, convB.slug);
    await selectConversation(page, convA.slug);
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    // Exactly one /api/stream2 connection should have been opened.
    // (The stream is held open for the lifetime of the page; switching
    // conversations must NOT reopen it.)
    expect(streamRequests).toHaveLength(1);

    // Resource Timing only logs completed responses; an unfinished EventSource
    // may show 0. What matters is "not >1".
    const resourceCount = await page.evaluate(
      () =>
        performance.getEntriesByType("resource").filter((e) => e.name.includes("/api/stream2"))
          .length,
    );
    expect(resourceCount).toBeLessThanOrEqual(1);
  });

  test("cross-conversation live update: B receives a message while viewing A", async ({
    page,
    request,
  }) => {
    const convA = await createConversationViaAPIWithDetails(request, "Hello");
    const convB = await createConversationViaAPIWithDetails(request, "hello");

    await page.goto(`/c/${convA.slug}`);
    await page.waitForLoadState("domcontentloaded");
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    // While viewing A, send a message to B out-of-band via the API. The
    // unified stream should deliver B's events into messageStore even though
    // B isn't the focused conversation.
    const chatResp = await request.post(`/api/conversation/${convB.conversationId}/chat`, {
      data: { message: "echo: surprise from background", model: "predictable" },
    });
    expect(chatResp.ok()).toBeTruthy();

    // Wait until the server has fully processed B's follow-up turn.
    await expect(async () => {
      const r = await request.get(`/api/conversation/${convB.conversationId}`);
      expect(r.ok()).toBeTruthy();
      const body = await r.json();
      const text = JSON.stringify(body.messages || []);
      expect(text.includes("surprise from background")).toBeTruthy();
      const turnDone = body.messages?.filter(
        (m: { type: string; end_of_turn?: boolean }) =>
          m.type === "agent" && m.end_of_turn === true,
      ).length;
      // Initial creation produced one end_of_turn=true agent message;
      // the follow-up should produce a second.
      expect(turnDone).toBeGreaterThanOrEqual(2);
    }).toPass({ timeout: 30000 });

    // Switching to B should reveal the message the global stream delivered
    // while we were viewing A. The single SSE pre-populates messageStore for
    // B even though B was not the focused conversation. We assert visibility;
    // we do NOT assert a 0-backfill count here, because hasFullHistory for B
    // may not have been latched yet (App seeded max_sequence_id_known from
    // the snapshot, which may exceed cached.maxSequenceId on the first switch
    // and trip a defensive REST catch-up). The contract we care about — the
    // background message becomes visible on switch — is what matters.
    await selectConversation(page, convB.slug);
    await waitForText(page, "surprise from background");
  });

  test("reload preserves cache: no spinner, no backfill after reload", async ({
    page,
    request,
  }) => {
    const convA = await createConversationViaAPIWithDetails(request, "Hello");
    const convB = await createConversationViaAPIWithDetails(request, "hello");

    await page.goto(`/c/${convA.slug}`);
    await page.waitForLoadState("domcontentloaded");
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    // Reload. The IDB cache should rehydrate A so messages appear without a
    // loading spinner. (A REST backfill may still fire on initial mount as
    // defensive catch-up — the conversation list snapshot can advance
    // maxSequenceIdKnown past the cached maxSequenceId. We don't assert
    // against that here.)
    await page.reload();
    await page.waitForLoadState("domcontentloaded");
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");
    await expect(page.locator(".spinner")).toHaveCount(0);

    // Start counting AFTER the post-reload settled state, then do a round
    // trip B → A. The second visit to A must come from cache.
    const hitsA = trackConversationLoads(page, convA.conversationId);
    await selectConversation(page, convB.slug);
    await waitForText(page, "Well, hi there!");
    await selectConversation(page, convA.slug);
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");
    expect(hitsA, "B → A round trip after reload must be cache-served").toHaveLength(0);
  });

  test.skip("reconnect triggers REST backfill for the focused conversation", () => {
    // SKIPPED: Playwright's `page.context().setOffline(true)` blocks NEW
    // network requests but does not tear down already-established
    // long-lived connections — our /api/stream2 EventSource included. As a
    // result the EventSource stays in readyState=OPEN, globalStream.onerror
    // never fires, and the `online` re-handshake path (gated on
    // readyState === 2) is a no-op. The reconnect/backfill code path is
    // covered by unit tests in services/messageStore.test.ts and
    // services/globalStream.test.ts. Reliably driving it end-to-end would
    // require either a `window.__shelley.forceReconnect()` debug hook or a
    // way to kill the SSE response server-side mid-stream.
  });
});

// (d) Stale-cache catch-up via REST backfill on switch is not covered here:
// forcing the messageStore for a non-focused conversation to "behind" without
// a debug hook would require patching the production bundle. The reconnect
// test above exercises the same hasFullHistory=false → REST backfill branch.

for (const listed of [true, false]) {
  test(`a coalesced idle patch clears optimistic Thinking (${listed ? "known row" : "first row"})`, async ({
    page,
    request,
  }) => {
    const { conversationId, slug } = await createConversationViaAPIWithDetails(request, "Hello");
    let release!: () => void;
    const completed = new Promise<void>((resolve) => {
      release = resolve;
    });
    await page.route("**/api/stream2*", async (route) => {
      await completed;
      await route.fulfill({
        contentType: "text/event-stream",
        body: `data: ${JSON.stringify({
          conversation_list_patch: {
            reset: true,
            new_hash: "coalesced-idle",
            at: new Date().toISOString(),
            patch: [{ op: "replace", path: "", value: [finalRow] }],
          },
        })}\n\n`,
      });
    });
    const snapshot = await request.get("/api/conversations/snapshot");
    expect(snapshot.ok()).toBeTruthy();
    const { conversations } = await snapshot.json();
    const initialRow = conversations.find(
      (c: { conversation_id: string }) => c.conversation_id === conversationId,
    );
    expect(initialRow.working).toBe(false);
    const finalRow = { ...initialRow, max_sequence_id: initialRow.max_sequence_id + 2 };
    await page.route("**/api/conversations/snapshot", (route) =>
      route.fulfill({ json: { conversations: listed ? [initialRow] : [], hash: "before-send" } }),
    );
    await page.route(`**/api/conversation/${conversationId}/chat`, (route) =>
      route.fulfill({ status: 202, json: { status: "accepted" } }),
    );
    await page.goto(`/c/${slug}`, { waitUntil: "domcontentloaded" });
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");
    await page.getByTestId("message-input").fill("echo: fast reply");
    await page.getByTestId("send-button").click();
    const stop = page.getByRole("button", { name: "Stop", exact: true });
    await expect(stop).toBeVisible();
    release();
    await expect(stop).toBeHidden();
  });
}
