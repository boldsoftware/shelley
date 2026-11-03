import { test, expect, type Locator } from '@playwright/test';
import { createConversationViaAPI, createConversationViaAPIWithDetails, mountAllToolCards } from './helpers';

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
    const registry = await request.get('/api/tools');
    expect(registry.ok()).toBeTruthy();
    const { tools } = await registry.json();
    expect(tools.map((tool: { name: string }) => tool.name)).not.toContain('keyword_search');

    const slug = await ensureSmorgasbord(request);

    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    // All tool results are already in the DB; wait for the UI to render them,
    // then mount the ones that are still cheap offscreen placeholders so the
    // whole set can be asserted without scrolling through it.
    await page.waitForFunction(
      () => document.querySelectorAll('[data-testid="tool-call-completed"]').length >= 15,
      undefined,
      { timeout: 30000 },
    );
    await mountAllToolCards(page);
    await expect(page.locator(".tool-status-icon")).toHaveCount(0);

    // The generic-shaped cards (.tool) are identified by their header summary;
    // each must render its specialized component's emoji. Assert on attachment
    // rather than visibility: the smorgasbord is taller than the viewport and
    // offscreen chunks are content-visibility: auto.
    const toolCard = (summary: string | RegExp): Locator =>
      page.locator('.tool').filter({ has: page.locator('.tool-command', { hasText: summary }) });
    const expectEmoji = async (card: Locator, emoji: string) => {
      await expect(card.first()).toBeAttached();
      await expect(card.first().locator('.tool-emoji')).toHaveText(emoji);
    };

    // bash renders the BashTool card, with the command in the header.
    const command = "echo 'hello from bash'";
    const bashCard = page.locator('.bash-tool').filter({ hasText: command }).first();
    await expect(bashCard).toBeAttached();
    await expect(bashCard.locator('.bash-tool-emoji')).toBeAttached();
    await expect(bashCard.locator('.bash-tool-command')).toContainText(command);

    // Thinking content appears inline (not a tool card).
    const thinkingContent = page.locator('.thinking-content').filter({ hasText: "I'm thinking about the best approach" });
    await expect(thinkingContent.first()).toBeVisible();
    await expect(thinkingContent.locator('text=💭').first()).toBeVisible();

    // Diff / image / iframe tools keep their own card shapes.
    const patchTool = page.locator('.patch-tool').first();
    await expect(patchTool).toBeAttached();
    await expect(patchTool.locator('.patch-tool-emoji')).toBeAttached();

    // A browser: screenshot action renders the ScreenshotTool card, same as the
    // standalone screenshot tool.
    const screenshotTool = page.locator('.screenshot-tool').filter({ hasText: /\.png$|screenshot/i }).first();
    await expect(screenshotTool).toBeAttached();
    await expect(page.locator('.screenshot-tool .screenshot-tool-emoji').filter({ hasText: '📷' }).first()).toBeAttached();

    const readImageTool = page.locator('.screenshot-tool').filter({ hasText: '/tmp/image.png' });
    await expect(readImageTool.first()).toBeAttached();
    await expect(readImageTool.locator('.screenshot-tool-emoji').filter({ hasText: '🖼️' }).first()).toBeAttached();

    // browser: screencast_stop -> ScreencastTool card.
    await expect(page.locator('.screencast-tool').first()).toBeAttached();

    // Spot-check the rest of the set.
    await expectEmoji(toolCard('https://example.com'), '🌐');
    await expectEmoji(toolCard('document.title'), '⚡');
    await expectEmoji(toolCard(/^pdf$/), '📄');
    // browser: pdf links the saved file through /api/read.
    const pdfHref = await page.locator('.pdf-tool-link').first().getAttribute('href');
    expect(pdfHref).toMatch(/^\/api\/read\?path=%2Ftmp%2Fshelley-pdfs%2F[0-9a-f-]+\.pdf$/);
    const pdfResponse = await request.get(pdfHref!);
    expect(pdfResponse.headers()['content-type']).toBe('application/pdf');
    await expectEmoji(toolCard(/console/i), '📋');
    await expectEmoji(toolCard('/tmp/test-prompt.txt'), '🤖');
    // The emulate/network/accessibility/profile families are folded into the
    // single "browser" tool via `<family>_<sub>` actions, and the old
    // standalone browser_* tool names must still render with their specialized
    // components (for conversations in existing DBs). Both spellings reach the
    // same component, so the per-family emoji is what proves the dispatch.
    await expectEmoji(toolCard(/^device iphone_14$/), '📱');
    await expectEmoji(toolCard(/^device ipad$/), '📱');
    await expectEmoji(toolCard(/^enable$/), '📡');
    await expectEmoji(toolCard(/^tree$/), '🌳');
    await expectEmoji(toolCard(/^metrics$/), '📊');

    // Summary lines carry no native title tooltip; expanding shows the full text.
    await expect(page.locator('.tool-command[title], .bash-tool-command[title]')).toHaveCount(0);
    await expect(page.locator('.tool-command:not(.change-dir-command) .ellipsis-start')).toHaveCount(0);

    // No tool falls back to GenericTool's gear.
    await expect(page.locator('.tool-emoji').filter({ hasText: '⚙️' })).toHaveCount(0);
  });

  test('audio transcription has a dedicated card and generic fallbacks identify themselves', async ({ page, request }) => {
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      'echo: tool card fixture',
    );
    const response = await request.get(`/api/conversation/${conversationId}`);
    expect(response.ok()).toBeTruthy();
    const body = await response.json();
    body.conversation.agent_working = false;
    body.messages = [
      {
        message_id: 'tool-card-calls',
        conversation_id: conversationId,
        sequence_id: 1,
        type: 'agent',
        llm_data: JSON.stringify({
          Role: 1,
          Content: [
            {
              ID: 'audio-transcription',
              Type: 5,
              ToolName: 'openai_audio_transcription',
              ToolInput: {
                file: '/tmp/shelley-uploads/planning-session.webm',
                model: 'gpt-transcribe',
                response_format: 'json',
                prompt_chars: 1234,
              },
            },
            {
              ID: 'audio-transcription-error',
              Type: 5,
              ToolName: 'openai_audio_transcription',
              ToolInput: {
                file: '/tmp/shelley-uploads/broken-recording.webm',
                model: 'gpt-transcribe',
                prompt_chars: 0,
              },
            },
            {
              ID: 'unknown-tool',
              Type: 5,
              ToolName: 'custom_tool_missing_ui_&_#_%_雪_with_a_very_long_name',
              ToolInput: { value: 42 },
            },
            {
              ID: 'unknown-browser-action',
              Type: 5,
              ToolName: 'browser',
              ToolInput: { action: 'future_action_&_#_%_雪_without_a_dedicated_component_name' },
            },
          ],
          EndOfTurn: false,
        }),
        created_at: '2026-09-22T12:00:00Z',
        generation: 1,
      },
      {
        message_id: 'tool-card-results',
        conversation_id: conversationId,
        sequence_id: 2,
        type: 'user',
        llm_data: JSON.stringify({
          Role: 0,
          Content: [
            {
              Type: 6,
              ToolUseID: 'audio-transcription',
              ToolResult: [
                {
                  Type: 2,
                  Text: JSON.stringify({
                    duration_ms: 5573,
                    model: 'gpt-transcribe',
                    text: 'We should ship the polished transcription card this week.',
                    timestamps_model: 'whisper-1',
                    timestamps_path:
                      '/tmp/shelley-uploads/planning-session.webm.transcript-timestamps.json',
                  }),
                },
              ],
              ToolUseStartTime: '2026-09-22T12:00:00Z',
              ToolUseEndTime: '2026-09-22T12:00:05.573Z',
            },
            {
              Type: 6,
              ToolUseID: 'audio-transcription-error',
              ToolError: true,
              ToolResult: [
                {
                  Type: 2,
                  Text: JSON.stringify({
                    duration_ms: 931,
                    error: 'The recording could not be transcribed.',
                    model: 'gpt-transcribe',
                  }),
                },
              ],
              ToolUseStartTime: '2026-09-22T12:00:05.600Z',
              ToolUseEndTime: '2026-09-22T12:00:06.531Z',
            },
            {
              Type: 6,
              ToolUseID: 'unknown-tool',
              ToolResult: [{ Type: 2, Text: 'unknown tool output' }],
              ToolUseStartTime: '2026-09-22T12:00:06Z',
              ToolUseEndTime: '2026-09-22T12:00:06.100Z',
            },
            {
              Type: 6,
              ToolUseID: 'unknown-browser-action',
              ToolResult: [{ Type: 2, Text: 'unknown browser action output' }],
              ToolUseStartTime: '2026-09-22T12:00:07Z',
              ToolUseEndTime: '2026-09-22T12:00:07.100Z',
            },
          ],
          EndOfTurn: false,
        }),
        created_at: '2026-09-22T12:00:08Z',
        generation: 1,
      },
    ];

    await page.route('**/api/stream2*', (route) => route.abort());
    await page.route(`**/api/conversation/${conversationId}`, (route) =>
      route.fulfill({ json: body }),
    );
    await page.goto(`/c/${slug}`);

    const transcriptions = page.locator('.audio-transcription-tool');
    await expect(transcriptions).toHaveCount(2);
    const transcription = transcriptions.filter({ hasText: 'planning-session.webm' });
    await expect(transcription).toBeVisible();
    await expect(transcription).toHaveAttribute('data-testid', 'tool-call-completed');
    await expect(transcription).toContainText('Audio transcription');
    await expect(transcription).toContainText('planning-session.webm');
    await expect(transcription).toContainText('gpt-transcribe');
    await expect(transcription).toContainText('5.6s');
    await expect(transcription).not.toContainText('duration_ms');
    await expect(transcription).not.toContainText(
      'We should ship the polished transcription card this week.',
    );
    expect(
      await transcription
        .locator('.tool-header')
        .evaluate((element) => element.scrollWidth <= element.clientWidth),
    ).toBe(true);

    await transcription.locator('.tool-header').click();
    await expect(transcription).toContainText(
      'We should ship the polished transcription card this week.',
    );
    await expect(transcription).toContainText('1,234 prompt characters');
    await expect(transcription).toContainText('whisper-1');
    await expect(transcription).toContainText('planning-session.webm.transcript-timestamps.json');
    expect(
      await transcription.evaluate((element) => element.scrollWidth <= element.clientWidth),
    ).toBe(true);

    const failedTranscription = transcriptions.filter({ hasText: 'broken-recording.webm' });
    await expect(failedTranscription).toContainText('failed');
    await failedTranscription.locator('.tool-header').click();
    await expect(failedTranscription).toContainText('Error:');
    await expect(failedTranscription).toContainText('The recording could not be transcribed.');
    await expect(failedTranscription).not.toContainText('Transcript:');

    const rawGenericSummary = page.locator('.tool-result-summary');
    expect(
      await rawGenericSummary.evaluate((element) => element.scrollWidth <= element.clientWidth),
    ).toBe(true);
    const browserGeneric = page
      .locator('.tool')
      .filter({ hasText: 'browser (future_action_&_#_%_雪_without_a_dedicated_component_name)' });
    expect(
      await browserGeneric
        .locator('.tool-header')
        .evaluate((element) => element.scrollWidth <= element.clientWidth),
    ).toBe(true);

    const rawWarning = rawGenericSummary.locator('a.generic-tool-warning');
    const browserWarning = browserGeneric.locator('a.generic-tool-warning');
    for (const [link, toolName] of [
      [rawWarning, 'custom_tool_missing_ui_&_#_%_雪_with_a_very_long_name'],
      [browserWarning, 'browser (future_action_&_#_%_雪_without_a_dedicated_component_name)'],
    ] as const) {
      await expect(link).toHaveAttribute(
        'aria-label',
        `GENERIC TOOL RESULT (Please report a bug) — missing UI for ${toolName} (opens in new tab)`,
      );
      await expect(link).toHaveAttribute('target', '_blank');
      await expect(link).toHaveAttribute('rel', 'noopener noreferrer');
      const href = await link.getAttribute('href');
      expect(href).toBeTruthy();
      const url = new URL(href!);
      expect(`${url.origin}${url.pathname}`).toBe(
        'https://github.com/boldsoftware/shelley/issues/new',
      );
      expect(url.searchParams.get('labels')).toBe('bug');
      expect(url.searchParams.get('title')).toBe(`Missing tool UI: ${toolName}`);
      expect(url.searchParams.get('body')).toContain(`**Tool:** \`${toolName}\``);
    }

    const rawDetails = rawGenericSummary.locator('xpath=ancestor::details');
    await expect(rawDetails).not.toHaveAttribute('open', '');
    const rawPopupPromise = page.waitForEvent('popup');
    await rawWarning.click({ noWaitAfter: true });
    (await rawPopupPromise).close();
    await expect(rawDetails).not.toHaveAttribute('open', '');
    await rawWarning.focus();
    await rawWarning.press('Space');
    await expect(rawDetails).not.toHaveAttribute('open', '');

    const browserPopupPromise = page.waitForEvent('popup');
    await browserWarning.click({ noWaitAfter: true });
    (await browserPopupPromise).close();
    await expect(browserGeneric.locator('.tool-details')).toBeHidden();

    const warnings = page.getByText('GENERIC TOOL RESULT (Please report a bug)', { exact: true });
    await expect(warnings).toHaveCount(2);
    await expect(warnings.first()).toBeVisible();
    await expect(warnings.last()).toBeVisible();
  });

  test('browser input errors are labeled and keep the model guidance visible', async ({ page, request }) => {
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      'echo: browser input errors',
    );
    const response = await request.get(`/api/conversation/${conversationId}`);
    expect(response.ok()).toBeTruthy();
    const body = await response.json();
    const retry = ' · model asked to retry';
    const missingAction = 'Invalid browser call: missing action';
    const invalidInput = 'Invalid browser call: input must be a JSON object';
    const actionType = 'Invalid browser call: action must be a string';
    const unsupported = (action: string) => `Invalid browser call: unsupported action "${action}"`;
    // Results mirror the backend's messages; legacy rows come from older builds.
    const cases: { id: string; input: unknown; label: string; error: boolean; text: string }[] = [
      { id: 'missing-action', input: { url: 'https://example.com' }, label: missingAction + retry, error: true,
        text: 'invalid browser call: "action" is required and must be a non-empty string. Choose an action from the browser schema and resend the call.' },
      { id: 'empty-action', input: { action: '' }, label: missingAction + retry, error: true,
        text: 'invalid browser call: "action" is required.' },
      { id: 'legacy-missing', input: { action: '' }, label: missingAction, error: true,
        text: 'unknown action: ""' },
      { id: 'string-input', input: '{"action":"eval"}', label: invalidInput + retry, error: true,
        text: 'invalid browser call: arguments were sent as a quoted JSON string. Decoded arguments: {"action":"eval"}. Resend an unquoted JSON object with a non-empty string "action" and field types matching the browser schema.' },
      { id: 'malformed-input', input: '{"action":"eval"', label: invalidInput + retry, error: true,
        text: 'invalid browser call: arguments were not valid JSON.' },
      { id: 'null-input', input: null, label: invalidInput + retry, error: true,
        text: 'invalid browser call: arguments were null.' },
      { id: 'array-input', input: [], label: invalidInput + retry, error: true,
        text: 'invalid browser call: arguments were a JSON array.' },
      { id: 'number-action', input: { action: 42 }, label: actionType + retry, error: true,
        text: 'invalid browser call: "action" must be a string, got 42.' },
      { id: 'null-action', input: { action: null }, label: actionType + retry, error: true,
        text: 'invalid browser call: "action" must be a string, got null.' },
      ...['click', 'emulate_bogus', 'network_bogus', 'accessibility_bogus', 'profile_bogus'].map((action) => ({
        id: action, input: { action }, label: unsupported(action) + retry, error: true,
        text: `invalid browser call: unknown action "${action}". Choose an action from the browser schema and resend the call. All supported actions: navigate, eval, screenshot.`,
      })),
      { id: 'width-type', input: { action: 'resize', width: 'wide' },
        label: 'Invalid browser call: wrong field type for resize' + retry, error: true,
        text: 'invalid browser call: field "width" must be an integer, got a JSON string.' },
      { id: 'await-type', input: { action: 'eval', expression: 'x', await: 'yes' },
        label: 'Invalid browser call: wrong field type for eval' + retry, error: true,
        text: 'invalid browser call: field "await" must be a boolean, got a JSON string.' },
      { id: 'legacy-unknown', input: { action: 'legacy_bogus' }, label: unsupported('legacy_bogus'), error: true,
        text: 'unknown action: "legacy_bogus"' },
    ];
    // Actions the backend accepted (or failed at runtime) but this UI lacks a
    // card for are missing UI, which keeps the bug-report warning.
    const missingUI = [
      { id: 'new-action-ok', action: 'brand_new_action', error: false, text: 'worked' },
      { id: 'new-action-failed', action: 'other_new_action', error: true, text: 'failed to start browser' },
    ];
    const validFamilies = [
      { action: 'emulate_help', emoji: '📱' },
      { action: 'network_help', emoji: '📡' },
      { action: 'accessibility_help', emoji: '🌳' },
      { action: 'profile_help', emoji: '📊' },
    ];
    const calls = [
      ...cases.map(({ id, input }) => ({ ID: id, Type: 5, ToolName: 'browser', ToolInput: input })),
      ...missingUI.map(({ id, action }) => ({ ID: id, Type: 5, ToolName: 'browser', ToolInput: { action } })),
      { ID: 'valid-action', Type: 5, ToolName: 'browser', ToolInput: { action: 'eval', expression: 'document.title' } },
      ...validFamilies.map(({ action }) => ({ ID: action, Type: 5, ToolName: 'browser', ToolInput: { action } })),
    ];
    const result = (id: string, error: boolean, text: string) => ({
      Type: 6, ToolUseID: id, ToolError: error, ToolResult: [{ Type: 2, Text: text }],
    });
    const results = [
      ...cases.map(({ id, error, text }) => result(id, error, text)),
      ...missingUI.map(({ id, error, text }) => result(id, error, text)),
      result('valid-action', false, 'Fixture title'),
      ...validFamilies.map(({ action }) => result(action, false, `Fixture ${action}`)),
    ];
    body.conversation.agent_working = false;
    body.messages = [
      {
        message_id: 'browser-error-calls', conversation_id: conversationId,
        sequence_id: 1, type: 'agent', generation: 1, created_at: '2026-10-11T12:00:00Z',
        llm_data: JSON.stringify({ Role: 1, Content: calls, EndOfTurn: false }),
      },
      {
        message_id: 'browser-error-results', conversation_id: conversationId,
        sequence_id: 2, type: 'user', generation: 1, created_at: '2026-10-11T12:00:01Z',
        llm_data: JSON.stringify({ Role: 0, Content: results, EndOfTurn: false }),
      },
    ];
    await page.route('**/api/stream2*', (route) => route.abort());
    await page.route(`**/api/conversation/${conversationId}`, (route) =>
      route.fulfill({ json: body }),
    );
    await page.goto(`/c/${slug}`);
    await expect(page.locator('[data-testid="tool-call-completed"]')).toHaveCount(calls.length);
    await mountAllToolCards(page);

    const invalidCards = page.locator('.tool').filter({ hasText: 'Invalid browser call:' });
    await expect(invalidCards).toHaveCount(cases.length);
    await expect(invalidCards.locator('.generic-tool-warning')).toHaveCount(0);
    for (let i = 0; i < cases.length; i++) {
      const card = invalidCards.nth(i);
      await expect(card.locator('.tool-command')).toHaveText(cases[i].label);
      await card.locator('.tool-header').click();
      await expect(card.locator('.tool-details')).toContainText('Output (Error):');
      await expect(card.locator('.tool-details')).toContainText(cases[i].text);
      if (typeof cases[i].input === 'string') {
        await expect(card.locator('.tool-code').first()).toHaveText(cases[i].input as string);
      }
    }

    for (const { action } of missingUI) {
      const card = page.locator('.tool').filter({
        has: page.locator('.tool-command', { hasText: `browser (${action})` }),
      });
      await expect(card.locator('.generic-tool-warning')).toHaveCount(1);
    }
    await expect(page.locator('.generic-tool-warning')).toHaveCount(missingUI.length);

    const validCard = page.locator('.tool').filter({
      has: page.locator('.tool-command', { hasText: 'document.title' }),
    });
    await expect(validCard.locator('.tool-emoji')).toHaveText('⚡');
    for (const { emoji } of validFamilies) {
      await expect(page.locator('.tool-emoji').filter({ hasText: emoji })).toHaveCount(1);
    }
  });

  test('generic tool audit stays inside its card on mobile', async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 800 });
    await page.goto('/new');
    await page.locator('body').evaluate((body) => {
      body.innerHTML = `
        <div style="padding: 16px">
          <details class="tool-result-details">
            <summary class="tool-result-summary">
              <div class="tool-result-meta">
                <div class="tool-result-primary flex space-x-2">
                  <svg class="chat-tool-icon"></svg>
                  <div class="generic-tool-result-summary">
                    <div class="generic-tool-result-heading">
                      <span class="tool-result-name text-sm font-medium text-blue">
                        custom_tool_missing_ui
                      </span>
                      <a
                        class="generic-tool-warning"
                        href="https://github.com/boldsoftware/shelley/issues/new"
                        target="_blank"
                        rel="noopener noreferrer"
                      >
                        GENERIC TOOL RESULT (Please report a bug)
                      </a>
                    </div>
                    <span class="tool-result-status text-xs">
                      unknown tool output that must remain inside the card
                    </span>
                  </div>
                </div>
                <div class="tool-result-time"></div>
              </div>
            </summary>
          </details>
        </div>
      `;
    });

    const card = page.locator('.tool-result-details');
    const summary = page.locator('.tool-result-summary');
    const status = page.locator('.tool-result-status');
    const warning = page.locator('.generic-tool-warning');
    await expect(card).toBeVisible();
    await expect(warning).toBeVisible();
    expect(await summary.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(
      true,
    );
    for (const element of [status, warning]) {
      const elementBox = await element.boundingBox();
      const summaryBox = await summary.boundingBox();
      expect((elementBox?.x ?? 0) + (elementBox?.width ?? 0)).toBeLessThanOrEqual(
        (summaryBox?.x ?? 0) + (summaryBox?.width ?? 0),
      );
    }
  });

  test('bash tool shows command in header', async ({ page, request }) => {
    const slug = await createConversationViaAPI(request, 'bash: unique-test-command-xyz123');
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    // The card renders inline and collapsed, with the command in its header.
    const bashToolWithOurCommand = page.locator('.bash-tool').filter({ hasText: 'unique-test-command-xyz123' });
    await expect(bashToolWithOurCommand).toBeVisible({ timeout: 15000 });
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

    await page.waitForFunction(
      () => document.querySelectorAll('[data-testid="tool-call-completed"]').length >= 15,
      undefined,
      { timeout: 30000 },
    );
    await mountAllToolCards(page);

    // The BrowserNavigateTool card shows the URL in its header; expanding it
    // shows the URL section (rendered as a link inside .tool-code).
    const navigateCard = page
      .locator('.tool')
      .filter({ has: page.locator('.tool-command', { hasText: 'https://example.com' }) })
      .first();
    await navigateCard.scrollIntoViewIfNeeded();
    await expect(navigateCard).toContainText('https://example.com');
    await navigateCard.locator('.tool-header').click();
    await expect(
      navigateCard.locator('.tool-code').filter({ hasText: 'https://example.com' }).first(),
    ).toBeVisible();
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
      () => document.querySelectorAll('[data-testid="tool-call-completed"]').length >= 15,
      undefined,
      { timeout: 30000 },
    );
    await mountAllToolCards(page);

    // Get every tool card's emoji and check its computed font-size.
    const emojiSizes = await page.$$eval('.tool-emoji, .bash-tool-emoji, .patch-tool-emoji, .screenshot-tool-emoji', (elements) => elements.map((el) => window.getComputedStyle(el).fontSize));

    // All emojis should be 1rem (16px by default)
    // Check that all sizes are the same
    const uniqueSizes = new Set(emojiSizes);
    expect(uniqueSizes.size).toBe(1);

    // Verify the size is 16px (1rem)
    expect(emojiSizes[0]).toBe('16px');
  });
});
