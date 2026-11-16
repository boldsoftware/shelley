import { test, expect } from '@playwright/test';

test.describe('Shelley Smoke Tests', () => {
  test('page loads successfully', async ({ page }) => {
    await page.goto('/new');
    await page.waitForLoadState('domcontentloaded');
    
    // Just verify the page loads with a title
    const title = await page.title();
    expect(title).toBe('Shelley Agent');
  });

  test('welcome links point to the source and the customization section', async ({ page }) => {
    await page.goto('/new');
    await page.waitForLoadState('domcontentloaded');

    const welcome = page.locator('.chat-welcome-text');
    await expect(welcome.getByRole('link', { name: 'open-source' })).toHaveAttribute(
      'href',
      'https://github.com/boldsoftware/shelley/',
    );
    await expect(welcome.getByRole('link', { name: 'can be customized' })).toHaveAttribute(
      'href',
      'https://shelley.dev/#customization',
    );
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
  
  test('message input auto-grows without synchronous layout reads', async ({ page }) => {
    await page.goto('/new');
    await page.waitForLoadState('domcontentloaded');

    const messageInput = page.getByTestId('message-input');
    await expect(messageInput).toBeVisible();
    const initialHeight = await messageInput.evaluate((el) => el.getBoundingClientRect().height);

    await page.evaluate(() => {
      const state = window as Window & { __textareaScrollHeightReads?: number };
      const scrollHeight = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollHeight');
      if (!scrollHeight?.get) throw new Error('Element.scrollHeight getter not found');
      state.__textareaScrollHeightReads = 0;
      Object.defineProperty(Element.prototype, 'scrollHeight', {
        configurable: scrollHeight.configurable,
        enumerable: scrollHeight.enumerable,
        get() {
          if (this instanceof HTMLElement && this.classList.contains('message-textarea')) {
            state.__textareaScrollHeightReads = (state.__textareaScrollHeightReads || 0) + 1;
          }
          return scrollHeight.get!.call(this);
        },
      });
    });

    await messageInput.fill('one\ntwo\nthree\nfour\nfive\nsix');
    await expect
      .poll(() => messageInput.evaluate((el) => el.getBoundingClientRect().height))
      .toBeGreaterThan(initialHeight);
    await expect
      .poll(() =>
        page.evaluate(
          () =>
            (window as Window & { __textareaScrollHeightReads?: number })
              .__textareaScrollHeightReads || 0,
        ),
      )
      .toBe(0);

    await messageInput.fill(Array.from({ length: 20 }, (_, i) => `line ${i}`).join('\n'));
    await expect
      .poll(() => messageInput.evaluate((el) => el.getBoundingClientRect().height))
      .toBeLessThanOrEqual(202);
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
