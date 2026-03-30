import { test, expect } from '@playwright/test';

test.describe('Scroll behavior', () => {
  test('shows scroll-to-bottom button when scrolled up, auto-scrolls when at bottom', async ({ page }) => {
    // Navigate to app
    await page.goto('/');
    
    // Wait for the app to load
    await page.waitForSelector('[data-testid="message-input"]');
    
    // Send multiple messages to create scrollable content
    const input = page.locator('[data-testid="message-input"]');
    const sendButton = page.locator('[data-testid="send-button"]');
    
    // Send a message that generates multiple tool calls to create enough content
    await input.fill('tool bash ls');
    await sendButton.click();
    
    // Wait for agent to finish
    await page.waitForSelector('[data-testid="agent-thinking"]', { state: 'hidden', timeout: 30000 });
    
    // Send more messages to ensure we have scrollable content
    for (let i = 0; i < 3; i++) {
      await input.fill(`echo message ${i}`);
      await sendButton.click();
      await page.waitForSelector('[data-testid="agent-thinking"]', { state: 'hidden', timeout: 30000 });
    }
    
    // Get the messages container
    const messagesContainer = page.locator('.messages-container');
    
    // Scroll up to the top
    await messagesContainer.evaluate((el) => {
      el.scrollTop = 0;
    });
    
    // Wait a moment for scroll event to be processed
    await page.waitForTimeout(200);
    
    // Verify scroll-to-bottom button appears
    const scrollButton = page.locator('.scroll-to-bottom-button');
    await expect(scrollButton).toBeVisible();
    
    // Click the button
    await scrollButton.click();
    
    // Wait for scroll animation
    await page.waitForTimeout(500);
    
    // Button should disappear
    await expect(scrollButton).not.toBeVisible();
    
    // Send another message - should auto-scroll since we're at bottom
    await input.fill('echo final message');
    await sendButton.click();
    
    // Wait for response
    await page.waitForSelector('[data-testid="agent-thinking"]', { timeout: 30000 });
    
    // Button should not appear since we're following the conversation
    await expect(scrollButton).not.toBeVisible();
  });
});
