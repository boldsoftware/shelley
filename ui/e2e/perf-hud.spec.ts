import { test, expect, type Page } from "@playwright/test";
import {
  createConversationViaAPI,
  setPageFeatureFlag,
  startNewConversationFromHeader,
} from "./helpers";

async function waitForCachedHistory(page: Page, conversationId: string): Promise<void> {
  await page.waitForFunction(
    async (id) => {
      const req = indexedDB.open("shelley-messages");
      const db: IDBDatabase = await new Promise((resolve, reject) => {
        req.onsuccess = () => resolve(req.result);
        req.onerror = () => reject(req.error);
      });
      try {
        if (!db.objectStoreNames.contains("conversation_meta")) return false;
        const row = await new Promise<{ has_full_history?: boolean } | undefined>((resolve) => {
          const get = db
            .transaction("conversation_meta", "readonly")
            .objectStore("conversation_meta")
            .get(id);
          get.onsuccess = () => resolve(get.result);
          get.onerror = () => resolve(undefined);
        });
        return !!row?.has_full_history;
      } finally {
        db.close();
      }
    },
    conversationId,
    { timeout: 30000 },
  );
}

// The performance-hud feature flag overlays live recomputation counters
// (see ui/src/utils/perf.ts). The counters themselves are always collected
// and exposed at window.__shelleyPerf; the flag only controls the overlay.
test.describe("Performance HUD", () => {
  test("hidden by default, __shelleyPerf still available", async ({ page, request }) => {
    const slug = await createConversationViaAPI(request, "echo perf-hud-off");
    await page.goto(`/c/${slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

    await expect(page.locator(".perf-hud")).toHaveCount(0);
    const counters = await page.evaluate(() => {
      const perf = (window as unknown as { __shelleyPerf?: { snapshot(): object } }).__shelleyPerf;
      return perf ? Object.keys(perf.snapshot()) : null;
    });
    expect(counters).not.toBeNull();
    expect(counters!.length).toBeGreaterThan(0);
  });

  test("IndexedDB loads show status, memory loads render at once, both reach the HUD", async ({ page, request }) => {
    await setPageFeatureFlag(page, "performance-hud", true);

    const generated = await request.post("/debug/loremipsum?json=1", {
      form: { size: "medium", model: "predictable" },
    });
    expect(generated.ok()).toBeTruthy();
    const { conversation_id: conversationId } = await generated.json();
    const conversationResponse = await request.get(`/api/conversation/${conversationId}`);
    expect(conversationResponse.ok()).toBeTruthy();
    const conversationBody = await conversationResponse.json();
    const expectedMessages = (conversationBody.messages as unknown[]).length;
    const conversationSlug = conversationBody.conversation.slug as string;
    const expectedMessageText = `${expectedMessages.toLocaleString()} messages`;

    await page.goto(`/c/${conversationId}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await expect(page.getByTestId("message").first()).toBeVisible({ timeout: 30000 });
    await waitForCachedHistory(page, conversationId);

    const fullLoads: string[] = [];
    const fullLoadPath = `/api/conversation/${conversationId}`;
    page.on("request", (req) => {
      const url = new URL(req.url());
      if (url.pathname === fullLoadPath && !url.searchParams.has("last_sequence_id")) {
        fullLoads.push(req.url());
      }
    });

    await page.addInitScript(() => {
      const statuses: Array<{ text: string; messageElements: number }> = [];
      (
        window as unknown as {
          __shelleyLoadingStatuses: Array<{ text: string; messageElements: number }>;
        }
      ).__shelleyLoadingStatuses = statuses;
      const collect = () => {
        const text = document.querySelector(".conversation-loading")?.textContent?.trim();
        if (text && statuses.at(-1)?.text !== text) {
          statuses.push({
            text,
            messageElements: document.querySelectorAll('[data-testid="message"]').length,
          });
        }
      };
      new MutationObserver(collect).observe(document, {
        childList: true,
        characterData: true,
        subtree: true,
      });
    });

    await page.reload({ waitUntil: "domcontentloaded" });

    // A cache hit still has to mount thousands of DOM nodes. Keep the detailed
    // status in the DOM through that work instead of showing only the generic
    // spinner whose delayed timer cannot fire while WebKit's main thread is
    // blocked. A page-local MutationObserver is used because a long render can
    // prevent Playwright itself from polling while the status is painted.
    await expect(page.getByTestId("message").first()).toBeVisible({ timeout: 30000 });
    const statuses = await page.evaluate(
      () =>
        (
          window as unknown as {
            __shelleyLoadingStatuses?: Array<{ text: string; messageElements: number }>;
          }
        ).__shelleyLoadingStatuses ?? [],
    );
    expect(
      statuses.some(
        (status) => status.text.includes(expectedMessageText) && status.messageElements === 0,
      ),
    ).toBeTruthy();
    await expect(page.locator(".conversation-loading")).toHaveCount(0, { timeout: 30000 });
    expect(fullLoads).toHaveLength(0);

    const loads = await page.evaluate(() => window.__shelleyPerf?.loads() ?? []);
    expect(loads.at(-1)).toMatchObject({
      conversationId,
      source: "indexeddb",
      messages: expectedMessages,
    });
    expect(loads.at(-1)?.totalMs).toBeGreaterThan(0);
    expect(loads.at(-1)?.renderMs).toBeGreaterThan(0);

    const hud = page.locator(".perf-hud");
    await expect(hud.locator(".perf-hud-load").first()).toContainText("IndexedDB", {
      timeout: 5000,
    });

    // The expanded HUD overlays the drawer. Its height depends on how many
    // counters earlier actions populated, so collapse it before navigating.
    await hud.locator(".perf-hud-title").click();
    await expect(hud).toHaveClass(/collapsed/);

    await page.evaluate(() => {
      const statuses = (
        window as unknown as {
          __shelleyLoadingStatuses?: Array<{ text: string; messageElements: number }>;
        }
      ).__shelleyLoadingStatuses;
      if (statuses) statuses.length = 0;
    });
    await startNewConversationFromHeader(page);
    await expect(page).toHaveURL(/\/new$/);
    const conversationTitle = page
      .locator(".conversation-title")
      .getByText(conversationSlug, { exact: true });
    const openConversations = page.locator('button[aria-label="Open conversations"]');
    if (await openConversations.isVisible()) {
      await openConversations.click();
      await expect(page.locator(".drawer.open")).toBeVisible();
    } else {
      const expandSidebar = page.locator('button[aria-label="Expand sidebar"]');
      if (await expandSidebar.isVisible()) await expandSidebar.click();
    }
    await conversationTitle.scrollIntoViewIfNeeded();
    await expect(conversationTitle).toBeInViewport();
    await conversationTitle.click();
    await expect(page.getByTestId("message").first()).toBeVisible({ timeout: 30000 });

    const memoryStatuses = await page.evaluate(
      () =>
        (
          window as unknown as {
            __shelleyLoadingStatuses?: Array<{ text: string; messageElements: number }>;
          }
        ).__shelleyLoadingStatuses ?? [],
    );
    // A memory hit renders in the switch's own flush: no loading status.
    expect(memoryStatuses).toEqual([]);
    // The load is recorded once it has rendered, which on a busy machine can
    // trail the first message becoming visible; until then the latest entry
    // is still the IndexedDB load above.
    await expect
      .poll(() => page.evaluate(() => window.__shelleyPerf?.loads().at(-1)))
      .toMatchObject({
        conversationId,
        source: "memory",
        messages: expectedMessages,
      });
    await hud.locator(".perf-hud-title").click();
    await expect(hud.locator(".perf-hud-load").first()).toContainText("Memory");
  });

  test("flag shows the HUD with live counters", async ({ page, request }) => {
    await setPageFeatureFlag(page, "performance-hud", true);
    const slug = await createConversationViaAPI(request, "echo perf-hud-on");
    await page.goto(`/c/${slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

    const hud = page.locator(".perf-hud");
    await expect(hud).toBeVisible({ timeout: 10000 });
    // Loading a conversation mounts Message components, so the table should
    // list at least one counter row within a poll interval.
    await expect(hud.locator("tbody tr").first()).toBeVisible({ timeout: 5000 });
    await expect(hud.locator(".perf-hud-empty")).toHaveCount(0);

    // Collapse toggles to the mini summary. Click the title: the header's
    // right side holds reset/copy/pause buttons that stop propagation.
    await hud.locator(".perf-hud-title").click();
    await expect(hud).toHaveClass(/collapsed/);
    await expect(hud.locator(".perf-hud-mini")).toBeVisible();
    await hud.locator(".perf-hud-title").click();

    // Long tasks (>50ms main-thread blocks) get their own section. Reset so
    // page-load counters don't crowd the top-4 suspects list, register a
    // sentinel counter, then block the main thread; the sentinel should be
    // reported as a suspect for the resulting long task. The block must run
    // in a real event-loop task (setTimeout): work executed directly inside
    // a DevTools-protocol evaluate is not reported by the Longtask API.
    await page.evaluate(
      () =>
        new Promise<void>((resolve) => {
          const perf = (
            window as unknown as {
              __shelleyPerf: { reset(): void; count(name: string): void };
            }
          ).__shelleyPerf;
          perf.reset();
          perf.count("test.suspect");
          setTimeout(() => {
            const t0 = performance.now();
            while (performance.now() - t0 < 80) {
              /* block the main thread */
            }
            resolve();
          }, 0);
        }),
    );
    await expect(hud.locator(".perf-hud-longtask").first()).toBeVisible({ timeout: 5000 });
    await expect(
      hud.locator(".perf-hud-longtask-suspects", { hasText: "test.suspect" }),
    ).toBeVisible();
  });
});
