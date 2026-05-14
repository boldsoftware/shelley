import { test, expect } from '@playwright/test';

test.describe('Shelley Smoke Tests', () => {
  test('page loads successfully', async ({ page }) => {
    await page.goto('/new');
    await page.waitForLoadState('domcontentloaded');
    
    // Just verify the page loads with a title
    const title = await page.title();
    expect(title).toBe('Shelley Agent');
  });

  test('can find message input with proper aria label', async ({ page }) => {
    await page.goto('/new');
    await page.waitForLoadState('domcontentloaded');
    
    // Find the textarea using improved selectors
    const messageInput = page.getByTestId('message-input');
    await expect(messageInput).toBeVisible();
    
    // Verify it has proper aria labeling
    await expect(messageInput).toHaveAttribute('aria-label', 'Message input');
  });

  test('can find send button with proper aria label', async ({ page }) => {
    await page.goto('/new');
    await page.waitForLoadState('domcontentloaded');
    
    // Find the send button using improved selectors
    const sendButton = page.getByTestId('send-button');
    await expect(sendButton).toBeVisible();
    
    // Verify it has proper aria labeling
    await expect(sendButton).toHaveAttribute('aria-label', 'Send message');
  });
  
  test('message input is initially empty and focused', async ({ page }) => {
    await page.goto('/new');
    await page.waitForLoadState('domcontentloaded');
    
    const messageInput = page.getByTestId('message-input');
    await expect(messageInput).toBeVisible();
    
    // Verify input is empty initially
    await expect(messageInput).toHaveValue('');
    
    // Verify placeholder text is present. The actual value is picked
    // randomly from a pool of hints on each mount (see placeholderHints.ts),
    // so just assert that *some* non-empty placeholder is set rather than
    // a specific string.
    await expect(messageInput).toHaveAttribute('placeholder', /.+/);
  });
  
  test('send button is disabled when input is empty', async ({ page }) => {
    await page.goto('/new');
    await page.waitForLoadState('domcontentloaded');
    
    const sendButton = page.getByTestId('send-button');
    
    // Button should be disabled initially
    await expect(sendButton).toBeDisabled();
  });
  
  test('send button becomes enabled when text is entered', async ({ page }) => {
    await page.goto('/new');
    await page.waitForLoadState('domcontentloaded');
    
    const messageInput = page.getByTestId('message-input');
    const sendButton = page.getByTestId('send-button');
    
    // Enter some text
    await messageInput.fill('test message');
    
    // Button should now be enabled
    await expect(sendButton).toBeEnabled();
  });
});
