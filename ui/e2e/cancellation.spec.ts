import { test, expect } from '@playwright/test';

test.describe('Conversation Cancellation', () => {
  test('should cancel long-running command and show cancelled state after reload', async ({ page }) => {
    // Start the server and navigate to it
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    // Wait for the message input
    const input = page.getByTestId('message-input');
    await expect(input).toBeVisible({ timeout: 30000 });

    // Send a command that will take a long time (sleep 100 seconds)
    await input.fill('bash: sleep 100');

    const sendButton = page.getByTestId('send-button');
    await expect(sendButton).toBeVisible();
    await sendButton.click();

    // Wait for the agent to start working (thinking indicator appears)
    await expect(page.locator('[data-testid="agent-thinking"]')).toBeVisible({ timeout: 10000 });

    // Wait a bit for the tool to actually start executing
    await page.waitForTimeout(500);

    // Verify the cancel button appears when agent is working
    const cancelButton = page.locator('button:has-text("Stop")');
    await expect(cancelButton).toBeVisible();

    // Click the cancel button
    await cancelButton.click();

    // Wait for cancellation to complete (button should disappear)
    await expect(cancelButton).not.toBeVisible({ timeout: 5000 });

    // Verify the thinking indicator is gone
    await expect(page.locator('[data-testid="agent-thinking"]')).not.toBeVisible({ timeout: 5000 });

    // Verify we see the cancelled tool result
    await expect(page.locator('text=/cancelled/i').first()).toBeVisible({ timeout: 5000 });

    // Verify we see the [Operation cancelled] message
    await expect(page.locator('text=/\\[Operation cancelled\\]/i')).toBeVisible({ timeout: 5000 });

    // Now reload the page to verify state is preserved
    await page.reload();
    await page.waitForLoadState('domcontentloaded');

    // After reload, the agent should NOT be working
    await expect(page.locator('[data-testid="agent-thinking"]')).not.toBeVisible({ timeout: 2000 });

    // Cancel button should not be visible
    await expect(page.locator('button:has-text("Stop")')).not.toBeVisible();

    // The cancelled messages should still be visible
    await expect(page.locator('text=/cancelled/i').first()).toBeVisible();
    await expect(page.locator('text=/\\[Operation cancelled\\]/i')).toBeVisible();

    // Verify we can continue the conversation after cancellation
    await input.fill('echo: test after cancel');
    await input.press('Enter');

    // Should get a response (the echo response may come so fast the thinking indicator is never visible)
    await expect(page.locator('text=test after cancel').first()).toBeVisible({ timeout: 10000 });

    // Agent should not be working after response
    await expect(page.locator('[data-testid="agent-thinking"]')).not.toBeVisible({ timeout: 5000 });
  });

  test('should cancel without tool execution (text generation)', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    const input = page.getByTestId('message-input');
    await expect(input).toBeVisible({ timeout: 30000 });

    // Send a command that triggers a delay in text generation
    await input.fill('delay: 5');

    const sendButton = page.getByTestId('send-button');
    await sendButton.click();

    // Wait for agent to start working
    await expect(page.locator('[data-testid="agent-thinking"]')).toBeVisible({ timeout: 5000 });

    // Wait a moment then cancel
    await page.waitForTimeout(500);

    const cancelButton = page.locator('button:has-text("Stop")');
    await expect(cancelButton).toBeVisible();
    await cancelButton.click();

    // Wait for cancellation
    await expect(cancelButton).not.toBeVisible({ timeout: 5000 });
    await expect(page.locator('[data-testid="agent-thinking"]')).not.toBeVisible({ timeout: 5000 });

    // Reload and verify agent is not working
    await page.reload();
    await page.waitForLoadState('domcontentloaded');
    await expect(page.locator('[data-testid="agent-thinking"]')).not.toBeVisible({ timeout: 2000 });
  });

  test('should show correct state without reload', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    const input = page.getByTestId('message-input');
    await expect(input).toBeVisible({ timeout: 30000 });

    // Send a long-running command
    await input.fill('bash: sleep 50');

    const sendButton = page.getByTestId('send-button');
    await sendButton.click();

    // Wait for agent to start working
    await expect(page.locator('[data-testid="agent-thinking"]')).toBeVisible({ timeout: 10000 });
    await page.waitForTimeout(500);

    // Cancel
    const cancelButton = page.locator('button:has-text("Stop")');
    await cancelButton.click();

    // Agent should stop working immediately (without reload)
    await expect(page.locator('[data-testid="agent-thinking"]')).not.toBeVisible({ timeout: 5000 });
    await expect(cancelButton).not.toBeVisible();

    // Should be able to send another message immediately
    await input.fill('echo: after cancel');

    const sendButton2 = page.getByTestId('send-button');
    await sendButton2.click();

    // Wait for response - use .first() to handle multiple matches
    await expect(page.locator('text=after cancel').first()).toBeVisible({ timeout: 10000 });
  });
});
