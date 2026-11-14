import { expect, test } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

for (const width of [390, 1280]) {
  test(`TOC shows and refreshes chapters without a feature flag at ${width}px`, async ({ page, request }) => {
    const { slug, conversationId } = await createConversationViaAPIWithDetails(request, "echo: chapter navigation");
    const response = await request.get(`/api/conversation/${conversationId}`);
    expect(response.ok()).toBeTruthy();
    const { messages } = await response.json();
    const anchor = messages.find((message: { type: string }) => message.type === "user").message_id;
    let title = "Chapter navigation";
    await page.route(`**/api/conversation/${conversationId}/themes`, route => route.fulfill({ json: {
      chapters: [{ title, message_id: anchor }], through_message_id: anchor,
    } }));
    await page.setViewportSize({ width, height: 800 });
    await page.goto(`/c/${slug}`);
    await page.getByRole("button", { name: "Conversation table of contents", exact: true }).click();
    const chapter = page.locator(".toc-entry-chapter");
    await expect(chapter).toHaveText(title);
    title = "Retrospective heading";
    await page.evaluate(id => window.dispatchEvent(new CustomEvent("shelley-chapters-updated", { detail: id })), conversationId);
    await expect(chapter).toHaveText(title);
    await chapter.click();
    await expect(page).toHaveURL(new RegExp(`#m-${anchor.slice(0, 8)}$`));
  });
}
