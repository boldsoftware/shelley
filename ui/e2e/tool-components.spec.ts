import { test, expect } from '@playwright/test';
import { createConversationViaAPI } from './helpers';

test.describe('Tool Component Verification', () => {
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
    await page.waitForFunction(() => document.querySelectorAll('[data-testid="tool-call-completed"]').length >= 15, undefined, { timeout: 30000 });

    // Verify bash tool uses BashTool component (has bash-tool class)
    const bashTool = page.locator('.bash-tool').filter({ hasText: "echo 'hello from bash'" });
    await expect(bashTool).toBeVisible();
    await expect(bashTool.locator('.bash-tool-emoji')).toBeVisible();
    await expect(bashTool.locator('.bash-tool-command')).toBeVisible();

    // Verify shell tool reuses BashTool component (has bash-tool class)
    const shellTool = page.locator('.bash-tool').filter({ hasText: "echo 'hello from shell'" });
    await expect(shellTool).toBeVisible();
    await expect(shellTool.locator('.bash-tool-emoji')).toBeVisible();
    await expect(shellTool.locator('.bash-tool-command')).toBeVisible();

    // Verify thinking content appears (has thinking-content class with 💭 emoji)
    const thinkingContent = page.locator('.thinking-content').filter({ hasText: "I'm thinking about the best approach" });
    await expect(thinkingContent.first()).toBeVisible();
    await expect(thinkingContent.locator('text=💭').first()).toBeVisible();

    // Verify patch tool uses PatchTool component (has patch-tool class)
    const patchTool = page.locator('.patch-tool').first();
    await expect(patchTool).toBeVisible();
    await expect(patchTool.locator('.patch-tool-emoji')).toBeVisible();

    // Verify screenshot tool uses ScreenshotTool component (has screenshot-tool class)
    const screenshotTool = page.locator('.screenshot-tool').first();
    await expect(screenshotTool).toBeVisible();
    await expect(screenshotTool.locator('.screenshot-tool-emoji').filter({ hasText: '📷' })).toBeVisible();

    // Verify keyword_search tool uses KeywordSearchTool component (has tool class with search emoji)
    const keywordTool = page.locator('.tool').filter({ hasText: 'find all references' });
    await expect(keywordTool.first()).toBeVisible();
    await expect(keywordTool.locator('.tool-emoji').filter({ hasText: '🔍' }).first()).toBeVisible();

    // Verify browser_navigate tool uses BrowserNavigateTool component (has tool class with globe emoji and URL)
    const navigateTool = page.locator('.tool').filter({ hasText: 'https://example.com' });
    await expect(navigateTool.first()).toBeVisible();
    await expect(navigateTool.locator('.tool-emoji').filter({ hasText: '🌐' }).first()).toBeVisible();

    // Verify browser_eval tool uses BrowserEvalTool component (has tool class with lightning emoji)
    const evalTool = page.locator('.tool').filter({ hasText: 'document.title' });
    await expect(evalTool.first()).toBeVisible();
    await expect(evalTool.locator('.tool-emoji').filter({ hasText: '⚡' }).first()).toBeVisible();

    // Verify read_image tool uses ReadImageTool component (has screenshot-tool class with frame emoji)
    const readImageTool = page.locator('.screenshot-tool').filter({ hasText: '/tmp/image.png' });
    await expect(readImageTool.first()).toBeVisible();
    await expect(readImageTool.locator('.screenshot-tool-emoji').filter({ hasText: '🖼️' }).first()).toBeVisible();

    // Verify browser_recent_console_logs tool uses BrowserConsoleLogsTool component (has tool class with clipboard emoji)
    const consoleTool = page.locator('.tool').filter({ hasText: 'console logs' });
    await expect(consoleTool.first()).toBeVisible();
    await expect(consoleTool.locator('.tool-emoji').filter({ hasText: '📋' }).first()).toBeVisible();

    // Verify browser_emulate tool uses BrowserEmulateTool component (has tool class with device emoji)
    const emulateTool = page.locator('.tool').filter({ hasText: 'iphone_14' });
    await expect(emulateTool.first()).toBeVisible();
    await expect(emulateTool.locator('.tool-emoji').filter({ hasText: '📱' }).first()).toBeVisible();

    // Verify browser_network tool uses BrowserNetworkTool component (has tool class with network emoji)
    const networkTool = page.locator('.tool').filter({ hasText: 'enable' });
    await expect(networkTool.first()).toBeVisible();
    await expect(networkTool.locator('.tool-emoji').filter({ hasText: '📡' }).first()).toBeVisible();

    // Verify browser_accessibility tool uses BrowserAccessibilityTool component (has tool class with a11y emoji)
    const a11yTool = page.locator('.tool').filter({ hasText: 'tree' });
    await expect(a11yTool.first()).toBeVisible();
    await expect(a11yTool.locator('.tool-emoji').filter({ hasText: '♿' }).first()).toBeVisible();

    // Verify browser_profile tool uses BrowserProfileTool component (has tool class with profiling emoji)
    const profileTool = page.locator('.tool').filter({ hasText: 'metrics' });
    await expect(profileTool.first()).toBeVisible();
    await expect(profileTool.locator('.tool-emoji').filter({ hasText: '📊' }).first()).toBeVisible();

    // Verify llm_one_shot tool uses LlmOneShotTool component (has tool class with LLM emoji)
    const llmTool = page.locator('.tool').filter({ hasText: '/tmp/test-prompt.txt' });
    await expect(llmTool.first()).toBeVisible();
    await expect(llmTool.locator('.tool-emoji').filter({ hasText: '🤖' }).first()).toBeVisible();

    // Verify browser screencast_stop tool uses BrowserScreencastTool component (has screencast-tool class with 🎬 emoji)
    const screencastTool = page.locator('.screencast-tool').first();
    await expect(screencastTool).toBeVisible();
    await expect(screencastTool.locator('.screencast-tool-emoji').first()).toBeVisible();

    // CRITICAL: Verify that GenericTool (gear emoji ⚙️) is NOT used for any of these tools
    // We check that NO tool has the generic gear icon
    const genericToolGearEmojis = page.locator('.tool-emoji').filter({ hasText: '⚙️' });
    expect(await genericToolGearEmojis.count()).toBe(0);
  });

  test('bash tool shows command in header', async ({ page, request }) => {
    const slug = await createConversationViaAPI(request, 'bash: unique-test-command-xyz123');
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    // Wait for the bash tool to render
    await page.waitForFunction(() => document.body.textContent?.includes('unique-test-command-xyz123') ?? false, undefined, { timeout: 15000 });

    // Verify bash tool shows the command in the header (collapsed state)
    const bashToolWithOurCommand = page.locator('.bash-tool').filter({ hasText: 'unique-test-command-xyz123' });
    await expect(bashToolWithOurCommand).toBeVisible();
    const commandElement = bashToolWithOurCommand.locator('.bash-tool-command');
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

    await page.waitForFunction(() => document.querySelectorAll('[data-testid="tool-call-completed"]').length >= 15, undefined, { timeout: 30000 });

    // Verify browser_navigate tool shows URL in the header
    const navigateTool = page.locator('.tool').filter({ hasText: 'https://example.com' }).first();
    await expect(navigateTool.locator('.tool-command').filter({ hasText: 'https://example.com' })).toBeVisible();
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

    await page.waitForFunction(() => document.querySelectorAll('[data-testid="tool-call-completed"]').length >= 15, undefined, { timeout: 30000 });

    // Get all tool emojis and check their computed font-size
    const emojiSizes = await page.$$eval('.tool-emoji, .bash-tool-emoji, .patch-tool-emoji, .screenshot-tool-emoji', (elements) => elements.map((el) => window.getComputedStyle(el).fontSize));

    // All emojis should be 1rem (16px by default)
    // Check that all sizes are the same
    const uniqueSizes = new Set(emojiSizes);
    expect(uniqueSizes.size).toBe(1);

    // Verify the size is 16px (1rem)
    expect(emojiSizes[0]).toBe('16px');
  });
});
