import { test, expect } from '@playwright/test';
import { createConversationViaAPIWithDetails, stabilizeConversationSlug, waitForConversationSlug } from './helpers';

test.describe('Distill conversation', () => {
  test('spinner transitions to complete after distillation finishes', async ({ page, request }) => {
    // 1. Create a source conversation through the API with a stable route.
    const { conversationId: sourceId } = await createConversationViaAPIWithDetails(request, 'Hello');

    // 2. Distill the conversation via the API
    const distillResp = await request.post('/api/conversations/distill', {
      data: {
        source_conversation_id: sourceId,
        model: 'predictable'
      }
    });
    expect(distillResp.ok()).toBeTruthy();
    const { conversation_id: distilledId } = await distillResp.json();
    expect(distilledId).toBeTruthy();

    // 3. Wait for the distilled conversation to get a slug, then navigate
    //    directly to it (auto-selection may pick the source conversation).
    const initialSlug = await waitForConversationSlug(request, distilledId, 15000);
    const slug = await stabilizeConversationSlug(request, distilledId, initialSlug);

    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    // 4. Wait for the distill-complete indicator to appear.
    //    The distillation may already be done or still in progress;
    //    either way, we should eventually see "Distilled from".
    const completeIndicator = page.getByTestId('distill-complete');
    await expect(completeIndicator).toBeVisible({ timeout: 30000 });
    await expect(completeIndicator).toContainText('Distilled from');

    // 5. Verify the spinner is gone
    await expect(page.getByTestId('distill-in-progress')).toHaveCount(0);

    // 6. Verify the distilled user message appeared (scoped to the chat
    // messages area so the conversation drawer preview, which surfaces the
    // latest agent text, doesn't trigger a strict-mode multiple-match.)
    await expect(
      page.locator('.messages-container').locator('text=edit predictable.go to add a response for that one...'),
    ).toBeVisible({ timeout: 10000 });
  });
});
