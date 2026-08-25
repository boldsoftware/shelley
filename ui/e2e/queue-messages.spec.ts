import { test, expect } from '@playwright/test';
import { createConversationViaAPI } from './helpers';

// Seed a stable conversation route so reload-based checks don't depend on /new.
async function openConversation(page: import('@playwright/test').Page, request: import('@playwright/test').APIRequestContext) {
  const slug = await createConversationViaAPI(request, 'echo: queue seed');
  await page.goto(`/c/${slug}`);
  await page.waitForLoadState('domcontentloaded');
  const messageInput = page.getByTestId('message-input');
  await expect(messageInput).toBeVisible({ timeout: 30000 });
  return messageInput;
}

async function sendAndWaitForWorking(page: import('@playwright/test').Page, text: string) {
  const messageInput = page.getByTestId('message-input');
  await messageInput.fill(text);
  const sendButton = page.getByTestId('send-button');
  await expect(sendButton).toBeEnabled({ timeout: 5000 });
  await sendButton.tap();
  await expect(page.getByTestId('agent-thinking')).toBeVisible({ timeout: 30000 });
}

// Helper: queue a message via the split-button dropdown
async function queueMessage(page: import('@playwright/test').Page, text: string) {
  const messageInput = page.getByTestId('message-input');
  await messageInput.fill(text);
  const chevron = page.getByTestId('send-options-button');
  await expect(chevron).toBeEnabled({ timeout: 5000 });
  await expect(chevron).toBeVisible({ timeout: 5000 });
  await chevron.tap();
  const queueOption = page.getByTestId('queue-option');
  await expect(queueOption).toBeVisible({ timeout: 5000 });
  await queueOption.tap();
}

test.describe('Queue Messages', () => {
  test('split button appears when agent is working', async ({ page, request }) => {
    await openConversation(page, request);

    // Send a slow message so the agent stays working
    await sendAndWaitForWorking(page, 'delay: 15');

    // The chevron (send-options-button) should be visible
    const chevron = page.getByTestId('send-options-button');
    await expect(chevron).toBeVisible({ timeout: 5000 });
  });

  test('chevron stays active for compact-and-send when agent finishes', async ({ page, request }) => {
    await openConversation(page, request);

    // Use a short delay so it finishes quickly
    await sendAndWaitForWorking(page, 'delay: 2');

    const chevron = page.getByTestId('send-options-button');

    // Wait for agent to finish ("Delayed for 2 seconds" response)
    await page.waitForFunction(() => document.body.textContent?.includes('Delayed for 2 seconds') ?? false, undefined, { timeout: 30000 });

    // Type something so the send button is enabled
    const input = page.getByTestId('message-input');
    await input.fill('test');

    // The split button should still be there and, because a conversation is
    // open, remain enabled: "Compact and send" is available even when the
    // agent is idle. Queueing, however, is not (the agent isn't working).
    await expect(chevron).toBeVisible();
    await expect(chevron).toBeEnabled({ timeout: 10000 });
    await chevron.tap();
    await expect(page.getByTestId('compact-and-send-option')).toBeVisible({ timeout: 5000 });
    await expect(page.getByTestId('queue-option')).toHaveCount(0);

    // Send button should still be present and enabled
    const sendButton = page.getByTestId('send-button');
    await expect(sendButton).toBeVisible();
    await expect(sendButton).toBeEnabled();
  });

  test('can queue a message via dropdown', async ({ page, request }) => {
    await openConversation(page, request);
    await sendAndWaitForWorking(page, 'delay: 15');

    // Queue a message
    await queueMessage(page, 'echo: queued hello');

    // Verify the queued badge appears
    const queuedBadge = page.getByTestId('queued-badge');
    await expect(queuedBadge).toBeVisible({ timeout: 10000 });
  });

  test('queued message has cancel button', async ({ page, request }) => {
    await openConversation(page, request);
    await sendAndWaitForWorking(page, 'delay: 15');

    await queueMessage(page, 'echo: test cancel');

    const queuedBadge = page.getByTestId('queued-badge');
    await expect(queuedBadge).toBeVisible({ timeout: 10000 });

    const cancelButton = page.getByTestId('cancel-queued');
    await expect(cancelButton).toBeVisible();
  });

  test('can cancel a queued message', async ({ page, request }) => {
    await openConversation(page, request);
    await sendAndWaitForWorking(page, 'delay: 60');

    await queueMessage(page, 'echo: to be cancelled');

    const queuedBadge = page.getByTestId('queued-badge');
    await expect(queuedBadge).toBeVisible({ timeout: 10000 });

    // Click cancel and wait for the server to acknowledge deletion
    const cancelButton = page.getByTestId('cancel-queued');
    await expect(cancelButton).toBeVisible();
    const [cancelResp] = await Promise.all([page.waitForResponse((resp) => resp.url().includes('/cancel-queued') && resp.status() === 200, { timeout: 10000 }), cancelButton.tap()]);

    // The server deleted the message from the DB.
    // Reload the page to pick up the new state (the SSE stream sends
    // a metadata-only update which doesn't trigger a message list refresh).
    await page.reload();
    await page.waitForLoadState('domcontentloaded');
    await expect(page.getByTestId('message-input')).toBeVisible({ timeout: 30000 });

    // After reload, the cancelled message and its badge should be gone
    await expect(page.getByTestId('queued-badge')).toHaveCount(0, { timeout: 10000 });
    await expect(page.locator('text=to be cancelled')).toHaveCount(0, { timeout: 10000 });
  });

  test('cancelling restores all queued messages to the composer', async ({ page, request }) => {
    await openConversation(page, request);
    await sendAndWaitForWorking(page, 'delay: 60');

    await queueMessage(page, 'echo: first queued message');
    await queueMessage(page, 'echo: second queued message');
    await expect(page.getByTestId('queued-badge')).toHaveCount(2, { timeout: 10000 });

    const stopButton = page.getByRole('button', { name: 'Stop' });
    const [cancelResp] = await Promise.all([
      page.waitForResponse((resp) => resp.url().includes('/cancel') && resp.status() === 200, {
        timeout: 10000,
      }),
      stopButton.tap(),
    ]);
    expect(cancelResp.url()).toContain('/cancel');

    const messageInput = page.getByTestId('message-input');
    await expect(messageInput).toHaveValue('echo: first queued message\necho: second queued message');
    await expect(page.getByTestId('queued-badge')).toHaveCount(0, { timeout: 10000 });
  });

  test('cancelling does not replace text already in the composer', async ({ page, request }) => {
    await openConversation(page, request);
    await sendAndWaitForWorking(page, 'delay: 60');

    await queueMessage(page, 'echo: queued message');
    await expect(page.getByTestId('queued-badge')).toHaveCount(1, { timeout: 10000 });

    const messageInput = page.getByTestId('message-input');
    await messageInput.fill('keep this draft');
    await page.getByRole('button', { name: 'Stop' }).tap();

    await expect(messageInput).toHaveValue('keep this draft');
    await expect(page.getByTestId('queued-badge')).toHaveCount(0, { timeout: 10000 });
  });

  test('cancelling restores a queue request that has not been accepted', async ({ page, request }) => {
    await openConversation(page, request);
    await sendAndWaitForWorking(page, 'delay: 60');

    let releaseQueue: (() => void) | undefined;
    let markQueueStarted: (() => void) | undefined;
    const queueBlocked = new Promise<void>((resolve) => {
      releaseQueue = resolve;
    });
    const queueStarted = new Promise<void>((resolve) => {
      markQueueStarted = resolve;
    });
    await page.route('**/api/conversation/*/chat', async (route) => {
      const body = route.request().postDataJSON();
      if (body.queue) {
        markQueueStarted?.();
        await queueBlocked;
      }
      await route.continue();
    });

    void queueMessage(page, 'echo: pending steering');
    await queueStarted;

    await page.getByRole('button', { name: 'Stop' }).tap();
    await expect(page.getByTestId('message-input')).toHaveValue('echo: pending steering');

    releaseQueue?.();
  });

  test('queued message drains after agent finishes', async ({ page, request }) => {
    await openConversation(page, request);

    // The delay only has to outlast the queue interaction below, which takes a
    // few hundred ms (measured max 336ms over 5 local runs). It used to be 10s,
    // making this the slowest test in the suite at 14.8s -- nearly all of it
    // spent watching a sleep. 3s keeps ~9x margin for a loaded CI host; the
    // sibling test above already waits out a `delay: 2` the same way.
    await sendAndWaitForWorking(page, 'delay: 3');

    // Queue a message
    await queueMessage(page, 'echo: queued drain test');

    const queuedBadge = page.getByTestId('queued-badge');
    await expect(queuedBadge).toBeVisible({ timeout: 10000 });

    // Wait for the first agent response (delay finishes)
    await page.waitForFunction(() => document.body.textContent?.includes('Delayed for 3 seconds') ?? false, undefined, { timeout: 30000 });

    // After drain, queued badge should disappear
    await expect(queuedBadge).toBeHidden({ timeout: 15000 });

    // The agent processes the queued message — predictable echoes it back
    await page.waitForFunction(() => document.body.textContent?.includes('queued drain test') ?? false, undefined, { timeout: 30000 });
  });

  test('send button still works normally during agent working', async ({ page, request }) => {
    await openConversation(page, request);
    await sendAndWaitForWorking(page, 'delay: 15');

    // Type text and click the MAIN send button (not the dropdown)
    const messageInput = page.getByTestId('message-input');
    await messageInput.fill('echo: immediate send');
    const sendButton = page.getByTestId('send-button');
    await sendButton.tap();

    // Message appears as a normal user message
    await page.waitForFunction(() => document.body.textContent?.includes('echo: immediate send') ?? false, undefined, { timeout: 10000 });

    // No queued badge should exist
    const queuedBadges = page.getByTestId('queued-badge');
    await expect(queuedBadges).toHaveCount(0, { timeout: 3000 });
  });
});
