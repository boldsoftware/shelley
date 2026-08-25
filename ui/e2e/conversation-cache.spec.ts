import { test, expect } from '@playwright/test';
import { createConversationViaAPIWithDetails } from './helpers';

/**
 * Helper: wait for text to appear on the page.
 */
async function waitForText(page: import('@playwright/test').Page, text: string, timeout = 15000) {
  await page.waitForFunction((t) => document.body.textContent?.includes(t) ?? false, text, {
    timeout
  });
}

/**
 * Helper: select a conversation by clicking its item in the drawer.
 * Uses exact slug text matching to find the right item.
 */
async function selectConversation(page: import('@playwright/test').Page, slug: string) {
  const openDrawer = page.locator('button[aria-label="Open conversations"]');
  const mobile = await openDrawer.isVisible();
  if (mobile) {
    await openDrawer.click();
    await expect(page.locator('.drawer.open')).toBeVisible({ timeout: 5000 });
  } else {
    const expandSidebar = page.locator('button[aria-label="Expand sidebar"]');
    if (await expandSidebar.isVisible()) await expandSidebar.click();
  }
  const titleEl = page.locator('.conversation-title').getByText(slug, { exact: true });
  await titleEl.scrollIntoViewIfNeeded();
  await expect(titleEl).toBeInViewport({ timeout: 15000 });
  await titleEl.click();
  if (mobile) await expect(page.locator('.drawer.open')).toBeHidden({ timeout: 10000 });
}

/**
 * Wait until the conversation's encrypted IndexedDB row reports a complete
 * cached history.
 *
 * Persistence is write-behind (see messageStore's `inflight` set), so "the
 * messages are on screen" does NOT imply "the cache is on disk". Any test that
 * reloads or navigates away in order to exercise the cache has to wait for
 * this, or under parallel load it races the write and gets a cold cache.
 */
async function waitForCachedHistory(
  page: import('@playwright/test').Page,
  conversationId: string,
  timeout = 15000
) {
  await page.waitForFunction(
    async (id) => {
      const req = indexedDB.open('shelley-messages');
      const db: IDBDatabase = await new Promise((res, rej) => {
        req.onsuccess = () => res(req.result);
        req.onerror = () => rej(req.error);
      });
      try {
        if (!db.objectStoreNames.contains('conversation_meta')) return false;
        const row = await new Promise<{ has_full_history?: boolean } | undefined>((res) => {
          const r = db.transaction('conversation_meta', 'readonly')
            .objectStore('conversation_meta').get(id);
          r.onsuccess = () => res(r.result);
          r.onerror = () => res(undefined);
        });
        return !!row?.has_full_history;
      } finally {
        db.close();
      }
    },
    conversationId,
    { timeout }
  );
}

test.describe('Conversation cache', () => {
  test('switching conversations uses cache (no extra fetch on second visit)', async ({ page, request }) => {
    // Create two conversations with distinct messages
    const conv1 = await createConversationViaAPIWithDetails(request, 'Hello');
    const conv2 = await createConversationViaAPIWithDetails(request, 'hello');

    // Navigate directly to conv1 by slug
    await page.goto(`/c/${conv1.slug}`);
    await page.waitForLoadState('domcontentloaded');
    const messageInput = page.getByTestId('message-input');
    await expect(messageInput).toBeVisible({ timeout: 30000 });

    // Wait for conversation 1's response
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    // Switch to conversation 2
    await selectConversation(page, conv2.slug);
    await waitForText(page, 'Well, hi there!');

    // Now intercept network requests to verify cache hit.
    // We specifically watch for the full conversation load endpoint
    // (GET /api/conversation/<id> without any further path segments).
    const conversationLoadFetches: string[] = [];
    // Match exactly the full-load endpoint: /api/conversation/<id> with no sub-path
    const loadPattern = new RegExp(`/api/conversation/${conv1.conversationId}$`);
    page.on('request', (req) => {
      if (loadPattern.test(new URL(req.url()).pathname)) {
        conversationLoadFetches.push(req.url());
      }
    });

    // Switch back to conversation 1
    await selectConversation(page, conv1.slug);

    // Conversation 1 messages should be visible from cache
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    // Verify no new fetch was made for the full conversation load
    expect(conversationLoadFetches).toHaveLength(0);
  });

  test('cached conversation shows correct messages after streaming updates', async ({ page, request }) => {
    // Create a conversation
    const conv1 = await createConversationViaAPIWithDetails(request, 'Hello');

    // Navigate to it
    await page.goto(`/c/${conv1.slug}`);
    await page.waitForLoadState('domcontentloaded');
    const messageInput = page.getByTestId('message-input');
    await expect(messageInput).toBeVisible({ timeout: 30000 });
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    // Send a follow-up message
    await messageInput.fill('echo: follow up message');
    const sendButton = page.getByTestId('send-button');
    await sendButton.click();
    await waitForText(page, 'follow up message');

    // Create a second conversation and switch to it
    const conv2 = await createConversationViaAPIWithDetails(request, 'hello');

    // Reload to pick up the new conversation in the list
    await page.reload();
    await page.waitForLoadState('domcontentloaded');
    await expect(messageInput).toBeVisible({ timeout: 30000 });

    // Navigate to conv2
    await selectConversation(page, conv2.slug);
    await waitForText(page, 'Well, hi there!');

    // Switch back to conv1 — cache should have both original + follow-up
    await selectConversation(page, conv1.slug);
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");
    await waitForText(page, 'follow up message');
  });

  test('cache serves messages instantly without loading spinner', async ({ page, request }) => {
    // Create two conversations
    const conv1 = await createConversationViaAPIWithDetails(request, 'Hello');
    const conv2 = await createConversationViaAPIWithDetails(request, 'hello');

    // Navigate to conv1
    await page.goto(`/c/${conv1.slug}`);
    await page.waitForLoadState('domcontentloaded');
    const messageInput = page.getByTestId('message-input');
    await expect(messageInput).toBeVisible({ timeout: 30000 });
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    // Switch to conv2
    await selectConversation(page, conv2.slug);
    await waitForText(page, 'Well, hi there!');

    // Switch back to conv1 — should be instant (cache hit)
    await selectConversation(page, conv1.slug);

    // Verify no loading spinner is shown
    await expect(page.locator('.spinner')).toHaveCount(0);
    await expect(page.locator("text=Hello! I'm Shelley, your AI assistant.").first()).toBeVisible();
  });

  test('page reload serves history from the IndexedDB cache', async ({ page, request }) => {
    // The regression this guards: metadata mutators (setMaxSequenceIdKnown /
    // setConversation, which App pumps for EVERY conversation in the list on
    // startup) used to mark conversations "hydrated" without reading IndexedDB.
    // The disk cache was therefore never consulted on a fresh page load, so
    // every reload re-downloaded the whole conversation over REST and the cache
    // only ever helped for in-session conversation switches.
    const conv = await createConversationViaAPIWithDetails(request, 'Hello');

    await page.goto(`/c/${conv.slug}`);
    await page.waitForLoadState('domcontentloaded');
    await expect(page.getByTestId('message-input')).toBeVisible({ timeout: 30000 });
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    // Let the write-behind IndexedDB persistence land before reloading.
    await waitForCachedHistory(page, conv.conversationId);

    // Count full-conversation loads across the reload. The cache should make
    // this zero: a complete cached history that the conversation list agrees
    // is up to date needs no server round-trip at all.
    const fullLoads: string[] = [];
    const fullLoadPattern = new RegExp(`/api/conversation/${conv.conversationId}$`);
    page.on('request', (req) => {
      const url = new URL(req.url());
      if (fullLoadPattern.test(url.pathname) && !url.searchParams.has('last_sequence_id')) {
        fullLoads.push(req.url());
      }
    });

    await page.reload();
    await page.waitForLoadState('domcontentloaded');
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");

    expect(fullLoads).toHaveLength(0);

    // And the cache diagnostics agree it was a cache hit, not a reload.
    const stats = await page.evaluate(() => window.__shelleyCache?.stats() ?? {});
    expect(Object.keys(stats)).toContain('load.served_from_cache');
    expect(stats['load.full_rest'] ?? 0).toBe(0);
  });

  test('page reload restores a non-bottom reading position', async ({ page, request }) => {
    const generated = await request.post('/debug/loremipsum?json=1', {
      form: { size: '15', model: 'predictable' }
    });
    expect(generated.ok()).toBeTruthy();
    const { conversation_id: conversationId } = await generated.json();
    const slug = `loremipsum-15turns-${conversationId}`;

    await page.goto(`/c/${slug}`);
    await expect(page.getByTestId('message-input')).toBeVisible({ timeout: 30000 });
    await expect.poll(() => page.getByTestId('message').count()).toBeGreaterThan(20);

    const restoredScrollTop = 1000;
    const messagesContainer = page.locator('.messages-container');
    await messagesContainer.evaluate((element, top) => {
      element.scrollTop = top;
      element.dispatchEvent(new Event('scroll'));
    }, restoredScrollTop);
    await expect.poll(() => page.evaluate(
      (id) => localStorage.getItem(`shelley_scroll_${id}`),
      conversationId
    )).toBe(String(restoredScrollTop));

    await page.reload();
    await expect(page.getByTestId('message-input')).toBeVisible({ timeout: 30000 });
    await expect.poll(() => page.getByTestId('message').count()).toBeGreaterThan(20);
    await expect.poll(() => messagesContainer.evaluate(
      (element) => element.scrollTop
    )).toBeGreaterThan(restoredScrollTop - 100);
    await expect.poll(() => messagesContainer.evaluate(
      (element) => element.scrollTop
    )).toBeLessThan(restoredScrollTop + 100);
  });

  test('a stalled incremental refresh reveals hot cached history at the bottom', async ({ page, request }) => {
    await page.addInitScript(() => {
      const nativeEventSource = window.EventSource;
      const sources: EventSource[] = [];
      Object.defineProperty(window, '__testEventSources', { value: sources });
      Object.defineProperty(window, 'EventSource', {
        configurable: true,
        value: class extends nativeEventSource {
          constructor(url: string | URL, eventSourceInitDict?: EventSourceInit) {
            super(url, eventSourceInitDict);
            sources.push(this);
          }
        }
      });
    });
    const generated = await request.post('/debug/loremipsum?json=1', {
      form: { size: '15', model: 'predictable' }
    });
    expect(generated.ok()).toBeTruthy();
    const { conversation_id: conversationId } = await generated.json();
    const slug = `loremipsum-15turns-${conversationId}`;
    const other = await createConversationViaAPIWithDetails(request, 'Hello');

    await page.goto(`/c/${slug}`);
    await expect(page.getByTestId('message-input')).toBeVisible({ timeout: 30000 });
    await expect.poll(() => page.getByTestId('message').count()).toBeGreaterThan(20);
    await waitForCachedHistory(page, conversationId);

    const restoredScrollTop = 1000;
    const messagesContainer = page.locator('.messages-container');
    await messagesContainer.evaluate((element, top) => {
      element.scrollTop = top;
      element.dispatchEvent(new Event('scroll'));
    }, restoredScrollTop);
    await expect.poll(() => page.evaluate(
      (id) => localStorage.getItem(`shelley_scroll_${id}`),
      conversationId
    )).toBe(String(restoredScrollTop));
    await selectConversation(page, other.slug);
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");
    await waitForCachedHistory(page, other.conversationId);

    let sawOtherRefresh = false;
    page.on('request', (req) => {
      const url = new URL(req.url());
      if (url.pathname === `/api/conversation/${other.conversationId}` &&
          url.searchParams.has('last_sequence_id')) {
        sawOtherRefresh = true;
      }
    });
    await page.evaluate(() => {
      const sources = (window as Window & { __testEventSources?: EventSource[] }).__testEventSources;
      const source = sources?.at(-1);
      if (!source) throw new Error('global EventSource was not captured');
      source.close();
      window.dispatchEvent(new Event('online'));
    });
    const reconnectEvent = await request.post('/debug/loremipsum?json=1', {
      form: { size: '1', model: 'predictable' }
    });
    expect(reconnectEvent.ok()).toBeTruthy();
    await expect.poll(() => sawOtherRefresh, { timeout: 15000 }).toBeTruthy();

    let releaseTail = () => {};
    const tailGate = new Promise<void>((resolve) => {
      releaseTail = resolve;
    });
    let sawIncremental = false;
    await page.route(new RegExp(`/api/conversation/${conversationId}\\?`), async (route) => {
      if (!new URL(route.request().url()).searchParams.has('last_sequence_id')) {
        await route.continue();
        return;
      }
      sawIncremental = true;
      await tailGate;
      await route.continue();
    });

    try {
      await selectConversation(page, slug);
      await expect.poll(() => sawIncremental).toBeTruthy();
      // The network tail is still blocked, but the complete hot-memory prefix
      // must already be usable and unobscured. Explicit conversation-list
      // selection opens at the latest content rather than restoring an old
      // reading offset.
      await expect.poll(() => page.getByTestId('message').count()).toBeGreaterThan(20);
      await expect(page.locator('.conversation-loading-overlay')).toHaveCount(0);
      await expect.poll(() => messagesContainer.evaluate(
        (element) => element.scrollHeight - element.clientHeight - element.scrollTop
      )).toBeLessThan(120);
      await expect(page.locator('.scroll-to-bottom-button')).toHaveCount(0);
    } finally {
      releaseTail();
    }
  });

  test('messages added while the tab was closed arrive via an incremental fetch', async ({
    page,
    request,
  }) => {
    // With a complete cached history we should never re-download it wholesale
    // just to discover the tail: ask for ?last_sequence_id=N instead.
    const conv = await createConversationViaAPIWithDetails(request, 'Hello');

    await page.goto(`/c/${conv.slug}`);
    await page.waitForLoadState('domcontentloaded');
    await expect(page.getByTestId('message-input')).toBeVisible({ timeout: 30000 });
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");
    // Wait for the cache to actually be on disk. Observing the REST load is
    // not enough: persistence is write-behind, so navigating away here would
    // leave nothing cached and the return trip would do a full reload.
    await waitForCachedHistory(page, conv.conversationId);

    // Navigate away so the tab isn't streaming, then add a message server-side.
    await page.goto('about:blank');
    const chatResp = await request.post(`/api/conversation/${conv.conversationId}/chat`, {
      data: { message: 'echo: added while closed', model: 'predictable' },
    });
    expect(chatResp.ok()).toBeTruthy();
    await expect(async () => {
      const resp = await request.get(`/api/conversation/${conv.conversationId}`);
      const body = await resp.json();
      // The message text lives inside llm_data (JSON), so match on the
      // serialized payload rather than a specific field.
      expect(JSON.stringify(body.messages ?? [])).toContain('added while closed');
    }).toPass({ timeout: 30000 });

    const incremental: string[] = [];
    const fullLoads: string[] = [];
    const pattern = new RegExp(`/api/conversation/${conv.conversationId}$`);
    page.on('request', (req) => {
      const url = new URL(req.url());
      if (!pattern.test(url.pathname)) return;
      if (url.searchParams.has('last_sequence_id')) incremental.push(req.url());
      else fullLoads.push(req.url());
    });

    await page.goto(`/c/${conv.slug}`);
    await page.waitForLoadState('domcontentloaded');
    // Both the cached history and the newly-added tail must be present.
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");
    await waitForText(page, 'added while closed');

    expect(incremental.length).toBeGreaterThan(0);
    expect(fullLoads).toHaveLength(0);
  });
});
