import { test, expect } from '@playwright/test';
import { createConversationViaAPI, openToolPill, closeToolModal, setPageFeatureFlag } from './helpers';

test.describe('Tool Component Verification', () => {
  // Pill rendering is gated behind a feature flag; opt in for this suite.
  test.beforeEach(async ({ page }) => {
    await setPageFeatureFlag(page, 'tool-pills', true);
  });

  // Shared smorgasbord conversation (created once, reused by multiple tests).
  // The smorgasbord launches browser tools (chromedp) which need up to 60s
  // for the initial Chrome startup, so we only pay that cost once.
  let smorgasbordSlug: string;

  async function ensureSmorgasbord(request: any): Promise<string> {
    if (!smorgasbordSlug) {
      smorgasbordSlug = await createConversationViaAPI(request, 'tool smorgasbord', {
        agentTimeout: 150000
      });
    }
    return smorgasbordSlug;
  }

  test('all tools use custom components, not GenericTool', async ({ page, request }) => {
    test.setTimeout(180000);
    const slug = await ensureSmorgasbord(request);

    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    // All tool results are already in the DB; wait for the UI to render them.
    // Pillable tools render as compact pills; auto-expand tools (patch,
    // screenshot, read_image, output_iframe) still render inline.
    await page.waitForFunction(
      () =>
        document.querySelectorAll(
          '.tool-pill[data-testid="tool-call-completed"], .patch-tool[data-testid="tool-call-completed"], .screenshot-tool[data-testid="tool-call-completed"]',
        ).length >= 15,
      undefined,
      { timeout: 30000 },
    );

    // Helper: open a pill matching the (toolName, textFilter) pair, assert
    // that the modal's specialized component is present, then close.
    // Predictable's smorgasbord uses a single "browser" tool name for
    // every browser action, so a text filter is required for those.
    const verifyPill = async (
      toolName: string,
      textFilter: string | RegExp | null,
      modalAssertion: (modal: ReturnType<typeof page.locator>) => Promise<void>,
    ) => {
      let pill = page.locator(`.tool-pill[data-tool-name="${toolName}"]`);
      if (textFilter !== null) pill = pill.filter({ hasText: textFilter });
      const first = pill.first();
      await expect(first).toBeVisible();
      await first.scrollIntoViewIfNeeded();
      await first.click();
      const modal = page.locator('.tool-pill-expanded');
      await expect(modal).toBeVisible();
      await modalAssertion(modal);
      // Pill itself must NOT use the GenericTool gear icon.
      expect(await first.locator('.tool-pill-emoji').filter({ hasText: '⚙️' }).count()).toBe(0);
      await closeToolModal(page);
    };

    await verifyPill('bash', null, async (modal) => {
      const t = modal.locator('.bash-tool').filter({ hasText: "echo 'hello from bash'" });
      await expect(t).toBeVisible();
      // In the detail modal the disclosure header is hidden (the modal title
      // is the headline); the command shows in the expanded Command section.
      await expect(t.locator('.bash-tool-details')).toBeVisible();
      await expect(
        t.locator('.bash-tool-code').filter({ hasText: "echo 'hello from bash'" }).first(),
      ).toBeVisible();
    });

    await verifyPill('shell', null, async (modal) => {
      const t = modal.locator('.bash-tool').filter({ hasText: "echo 'hello from shell'" });
      await expect(t).toBeVisible();
      await expect(t.locator('.bash-tool-details')).toBeVisible();
      await expect(
        t.locator('.bash-tool-code').filter({ hasText: "echo 'hello from shell'" }).first(),
      ).toBeVisible();
    });

    // Thinking content appears inline (no pill).
    const thinkingContent = page.locator('.thinking-content').filter({ hasText: "I'm thinking about the best approach" });
    await expect(thinkingContent.first()).toBeVisible();
    await expect(thinkingContent.locator('text=💭').first()).toBeVisible();

    // patch / screenshot / read_image still render inline (auto-expand tools).
    const patchTool = page.locator('.patch-tool').first();
    await expect(patchTool).toBeVisible();
    await expect(patchTool.locator('.patch-tool-emoji')).toBeVisible();

    const screenshotTool = page.locator('.screenshot-tool').filter({ hasText: /\.png$|screenshot/i }).first();
    await expect(screenshotTool).toBeVisible();

    const readImageTool = page.locator('.screenshot-tool').filter({ hasText: '/tmp/image.png' });
    await expect(readImageTool.first()).toBeVisible();
    await expect(readImageTool.locator('.screenshot-tool-emoji').filter({ hasText: '🖼️' }).first()).toBeVisible();

    // A browser: screenshot action is an auto-expand tool too: it must render
    // inline as a ScreenshotTool card, never collapse into a pill.
    await expect(
      page.locator('.tool-pill[data-tool-name="browser"]').filter({ hasText: /screenshot/i }),
    ).toHaveCount(0);
    await expect(page.locator('.screenshot-tool .screenshot-tool-emoji').filter({ hasText: '📷' }).first()).toBeVisible();

    // browser: screencast_stop pill -> ScreencastTool widget in modal.
    await verifyPill('browser', 'screencast_stop', async (modal) => {
      await expect(modal.locator('.screencast-tool').first()).toBeVisible();
    });

    // Spot-check the rest of the pill set. Each pill's specialized
    // component must render inside its modal.
    await verifyPill('keyword_search', null, async (modal) => {
      await expect(modal.locator('.tool .tool-emoji').filter({ hasText: '🔍' }).first()).toBeAttached();
    });
    await verifyPill('browser', 'https://example.com', async (modal) => {
      await expect(modal.locator('.tool .tool-emoji').filter({ hasText: '🌐' }).first()).toBeAttached();
    });
    await verifyPill('browser', /\beval\b/, async (modal) => {
      await expect(modal.locator('.tool .tool-emoji').filter({ hasText: '⚡' }).first()).toBeAttached();
    });
    await verifyPill('browser', 'console_logs', async (modal) => {
      await expect(modal.locator('.tool .tool-emoji').filter({ hasText: '📋' }).first()).toBeAttached();
    });
    // The emulate/network/accessibility/profile families are now folded into
    // the single "browser" tool via `<family>_<sub>` actions. Verify they
    // render with their specialized component (correct emoji) under that name.
    await verifyPill('browser', 'emulate_device', async (modal) => {
      await expect(modal.locator('.tool .tool-emoji').filter({ hasText: '📱' }).first()).toBeAttached();
    });
    await verifyPill('browser', 'network_enable', async (modal) => {
      await expect(modal.locator('.tool .tool-emoji').filter({ hasText: '📡' }).first()).toBeAttached();
    });
    await verifyPill('browser', 'accessibility_tree', async (modal) => {
      await expect(modal.locator('.tool .tool-emoji').filter({ hasText: '♿' }).first()).toBeAttached();
    });
    await verifyPill('browser', 'profile_metrics', async (modal) => {
      await expect(modal.locator('.tool .tool-emoji').filter({ hasText: '📊' }).first()).toBeAttached();
    });
    // Backwards-compat: old standalone browser_* tool names must still render
    // with their specialized components (for conversations in existing DBs).
    await verifyPill('browser_emulate', null, async (modal) => {
      await expect(modal.locator('.tool .tool-emoji').filter({ hasText: '📱' }).first()).toBeAttached();
    });
    await verifyPill('browser_network', null, async (modal) => {
      await expect(modal.locator('.tool .tool-emoji').filter({ hasText: '📡' }).first()).toBeAttached();
    });
    await verifyPill('browser_accessibility', null, async (modal) => {
      await expect(modal.locator('.tool .tool-emoji').filter({ hasText: '♿' }).first()).toBeAttached();
    });
    await verifyPill('browser_profile', null, async (modal) => {
      await expect(modal.locator('.tool .tool-emoji').filter({ hasText: '📊' }).first()).toBeAttached();
    });
    await verifyPill('llm_one_shot', null, async (modal) => {
      await expect(modal.locator('.tool .tool-emoji').filter({ hasText: '🤖' }).first()).toBeAttached();
    });

    // No pill should be rendered with the GenericTool gear emoji.
    const genericPills = page.locator('.tool-pill .tool-pill-emoji').filter({ hasText: '⚙️' });
    expect(await genericPills.count()).toBe(0);
  });

  test('bash tool shows command in header', async ({ page, request }) => {
    const slug = await createConversationViaAPI(request, 'bash: unique-test-command-xyz123');
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    // Wait for the bash pill to render and open it.
    const pill = page.locator('.tool-pill[data-tool-name="bash"]').filter({ hasText: 'unique-test-command-xyz123' });
    await expect(pill).toBeVisible({ timeout: 15000 });
    await pill.click();
    const modal = page.locator('.tool-pill-expanded');
    await expect(modal).toBeVisible();

    // The detail modal opens already expanded; the disclosure header is
    // hidden, so the command shows in the details body's Command section.
    const bashToolWithOurCommand = modal.locator('.bash-tool').filter({ hasText: 'unique-test-command-xyz123' });
    await expect(bashToolWithOurCommand).toBeVisible();
    await expect(bashToolWithOurCommand.locator('.bash-tool-details')).toBeVisible();
    const commandElement = bashToolWithOurCommand
      .locator('.bash-tool-code')
      .filter({ hasText: 'unique-test-command-xyz123' })
      .first();
    await expect(commandElement).toBeVisible();
    const commandText = await commandElement.textContent();
    expect(commandText).toContain('unique-test-command-xyz123');
  });

  test('think tool shows thought prefix in header', async ({ page, request }) => {
    const slug = await createConversationViaAPI(request, 'think: This is a long thought that should be truncated in the header display');
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    // Wait for the thinking content to render
    await page.waitForFunction(() => document.body.textContent?.includes("I've considered my approach.") ?? false, undefined, { timeout: 15000 });

    // Verify thinking content shows the thought text with 💭 emoji
    const thinkingContent = page.locator('.thinking-content').filter({ hasText: 'This is a long thought' }).first();
    await expect(thinkingContent).toBeVisible();
    const thinkingText = await thinkingContent.textContent();
    expect(thinkingText).toContain('This is a long thought');
  });

  test('browser navigate tool shows URL in header', async ({ page, request }) => {
    test.setTimeout(180000);
    const slug = await ensureSmorgasbord(request);

    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    await page.waitForFunction(
      () =>
        document.querySelectorAll(
          '.tool-pill[data-testid="tool-call-completed"], .patch-tool[data-testid="tool-call-completed"], .screenshot-tool[data-testid="tool-call-completed"]',
        ).length >= 15,
      undefined,
      { timeout: 30000 },
    );

    // The browser navigate pill shows the URL inline; opening it
    // reveals the full BrowserNavigateTool card.
    const navigatePill = page.locator('.tool-pill').filter({ hasText: 'https://example.com' }).first();
    await expect(navigatePill).toBeVisible();
    await expect(navigatePill).toContainText('https://example.com');
    await navigatePill.scrollIntoViewIfNeeded();
    await navigatePill.click();
    const navigateModal = page.locator('.tool-pill-expanded');
    // The card header is hidden in the modal; the URL shows in the details
    // body's URL section (rendered as a link inside .tool-code).
    await expect(
      navigateModal.locator('.tool-code').filter({ hasText: 'https://example.com' }).first(),
    ).toBeVisible();
    await closeToolModal(page);
  });

  test('patch tool can be collapsed and expanded without errors', async ({ page, request }) => {
    const slug = await createConversationViaAPI(request, 'patch success');
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    // Wait for successful patch tool
    const patchTool = page.locator('.patch-tool[data-testid="tool-call-completed"]').filter({ hasText: 'test-patch-success.txt' }).first();
    await expect(patchTool).toBeVisible({ timeout: 15000 });

    // Get console errors before toggling
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));

    const header = patchTool.locator('.patch-tool-header');

    // The toggle button should exist and respond to clicks
    const toggle = patchTool.locator('.patch-tool-toggle');
    await expect(toggle).toBeVisible();

    // Click to collapse
    await header.click();
    await expect(patchTool.locator('.patch-tool-details')).toBeHidden();

    // Expand
    await header.click();
    await expect(patchTool.locator('.patch-tool-details')).toBeVisible({ timeout: 10000 });

    // Collapse again
    await header.click();
    await expect(patchTool.locator('.patch-tool-details')).toBeHidden();

    // Expand again
    await header.click();
    await expect(patchTool.locator('.patch-tool-details')).toBeVisible({ timeout: 10000 });

    // Check no Monaco model errors occurred
    const modelErrors = errors.filter((e) => e.includes('model') && e.includes('already exists'));
    expect(modelErrors).toHaveLength(0);
  });

  test('emoji sizes are consistent across all tools', async ({ page, request }) => {
    test.setTimeout(180000);
    const slug = await ensureSmorgasbord(request);

    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    await page.waitForFunction(
      () =>
        document.querySelectorAll(
          '.tool-pill[data-testid="tool-call-completed"], .patch-tool[data-testid="tool-call-completed"], .screenshot-tool[data-testid="tool-call-completed"]',
        ).length >= 15,
      undefined,
      { timeout: 30000 },
    );

    // Get all visible *inline* tool emojis (auto-expand tools and any
    // tool widgets opened in modals) and check their computed
    // font-size. Tool pills intentionally use a smaller emoji and are
    // excluded from this size-consistency check.
    const emojiSizes = await page.$$eval('.tool-emoji, .bash-tool-emoji, .patch-tool-emoji, .screenshot-tool-emoji', (elements) => elements.map((el) => window.getComputedStyle(el).fontSize));

    // All emojis should be 1rem (16px by default)
    // Check that all sizes are the same
    const uniqueSizes = new Set(emojiSizes);
    expect(uniqueSizes.size).toBe(1);

    // Verify the size is 16px (1rem)
    expect(emojiSizes[0]).toBe('16px');
  });
});
