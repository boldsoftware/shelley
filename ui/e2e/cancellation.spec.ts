import { test, expect } from '@playwright/test';
import { createConversationViaAPI, openToolPill, closeToolModal, setPageFeatureFlag } from './helpers';

// Cancellation tests reload the page and inspect global state (sidebar),
// so they must not run in parallel with other tests.
test.describe.configure({ mode: 'serial' });

// Pill rendering is gated behind a feature flag; opt in for this suite.
test.beforeEach(async ({ page }) => {
  await setPageFeatureFlag(page, 'tool-pills', true);
});

async function openConversation(page: import('@playwright/test').Page, request: import('@playwright/test').APIRequestContext) {
  const slug = await createConversationViaAPI(request, 'echo: cancellation seed');
  await page.goto(`/c/${slug}`);
  await page.waitForLoadState('domcontentloaded');

  const input = page.getByTestId('message-input');
  await expect(input).toBeVisible({ timeout: 30000 });
  return input;
}

// The running bash tool now surfaces as a compact pill with a
// spinner. The full BashTool card lives inside a modal that opens
// when the pill is clicked.
async function waitForRunningBashPill(page: import('@playwright/test').Page) {
  const runningPill = page.locator('.tool-pill[data-testid="tool-call-running"][data-tool-name="bash"]');
  await expect(runningPill.first()).toBeVisible({ timeout: 10000 });
  return runningPill.first();
}

test.describe('Conversation Cancellation', () => {
  test('should cancel long-running command and show cancelled state after reload', async ({ page, request }) => {
    const input = await openConversation(page, request);

    // Send a command that will take a long time (sleep 100 seconds)
    await input.fill('bash: sleep 100');

    const sendButton = page.getByTestId('send-button');
    await expect(sendButton).toBeVisible();
    await sendButton.click();

    const thinkingIndicator = page.getByTestId('agent-thinking');
    await expect(thinkingIndicator).toBeVisible({ timeout: 10000 });
    await waitForRunningBashPill(page);

    // Verify the cancel button appears when agent is working
    const cancelButton = page.locator('.status-stop-button');
    await expect(cancelButton).toBeVisible();

    // Click the cancel button
    await cancelButton.click();

    // Wait for cancellation to complete (button should disappear)
    await expect(cancelButton).not.toBeVisible({ timeout: 5000 });

    // Verify the thinking indicator is gone
    await expect(thinkingIndicator).toBeHidden({ timeout: 5000 });

    // Verify we see the cancelled tool result (open the pill to
    // reveal the BashTool card inside the modal).
    {
      const modal = await openToolPill(page, 'sleep');
      await expect(modal.locator('.bash-tool .bash-tool-cancelled')).toBeVisible({ timeout: 5000 });
      await closeToolModal(page);
    }

    // Verify we see the [Operation cancelled] message in the chat messages
    // (scoped to .messages-container so the conversation drawer preview row,
    // which now also shows the latest agent text, doesn't cause a strict-mode
    // multiple-match violation).
    await expect(page.locator('.messages-container').locator('text=/\\[Operation cancelled\\]/i')).toBeVisible({ timeout: 5000 });

    // Now reload the page to verify state is preserved
    await page.reload();
    await page.waitForLoadState('domcontentloaded');
    const reloadedInput = page.getByTestId('message-input');
    await expect(reloadedInput).toBeVisible({ timeout: 30000 });

    // After reload, the agent should NOT be working
    await expect(page.getByTestId('agent-thinking')).toBeHidden({ timeout: 2000 });

    // Cancel button should not be visible
    await expect(page.locator('.status-stop-button')).toBeHidden();

    // The cancelled messages should still be visible (open the pill).
    {
      const modal = await openToolPill(page, 'sleep');
      await expect(modal.locator('.bash-tool .bash-tool-cancelled')).toBeVisible();
      await closeToolModal(page);
    }
    await expect(page.locator('.messages-container').locator('text=/\\[Operation cancelled\\]/i')).toBeVisible();

    // Verify we can continue the conversation after cancellation
    await reloadedInput.fill('echo: test after cancel');
    // Ctrl+Enter submits regardless of mobile Enter-for-newline behavior.
    await reloadedInput.press('ControlOrMeta+Enter');

    // Should get a response (the echo response may come so fast the thinking indicator is never visible)
    await expect(page.locator('text=test after cancel').first()).toBeVisible({ timeout: 10000 });

    // Agent should not be working after response
    await expect(page.getByTestId('agent-thinking')).toBeHidden({ timeout: 5000 });
  });

  test('should cancel without tool execution (text generation)', async ({ page, request }) => {
    const input = await openConversation(page, request);

    // Send a command that triggers a delay in text generation
    await input.fill('delay: 5');

    const sendButton = page.getByTestId('send-button');
    await sendButton.click();

    // Wait for agent to start working
    const thinkingIndicator = page.getByTestId('agent-thinking');
    await expect(thinkingIndicator).toBeVisible({ timeout: 5000 });

    const cancelButton = page.locator('.status-stop-button');
    await expect(cancelButton).toBeVisible();
    await cancelButton.click();

    // Wait for cancellation
    await expect(cancelButton).toBeHidden({ timeout: 5000 });
    await expect(thinkingIndicator).toBeHidden({ timeout: 5000 });

    // Reload and verify agent is not working
    await page.reload();
    await page.waitForLoadState('domcontentloaded');
    await expect(page.getByTestId('message-input')).toBeVisible({ timeout: 30000 });
    await expect(page.getByTestId('agent-thinking')).toBeHidden({ timeout: 2000 });
  });

  test('should show correct state without reload', async ({ page, request }) => {
    const input = await openConversation(page, request);

    // Send a long-running command
    await input.fill('bash: sleep 50');

    const sendButton = page.getByTestId('send-button');
    await sendButton.click();

    // Wait for agent to start working
    const thinkingIndicator = page.getByTestId('agent-thinking');
    await expect(thinkingIndicator).toBeVisible({ timeout: 10000 });
    await waitForRunningBashPill(page);

    // Cancel
    const cancelButton = page.locator('.status-stop-button');
    await cancelButton.click();

    // Agent should stop working immediately (without reload)
    await expect(thinkingIndicator).toBeHidden({ timeout: 5000 });
    await expect(cancelButton).toBeHidden();

    // Should be able to send another message immediately
    await input.fill('echo: after cancel');

    const sendButton2 = page.getByTestId('send-button');
    await sendButton2.click();

    // Wait for response - use .first() to handle multiple matches
    await expect(page.locator('text=after cancel').first()).toBeVisible({ timeout: 10000 });
  });
});
