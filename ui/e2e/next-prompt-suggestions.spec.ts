import { expect, test, type APIRequestContext, type Locator, type Page } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { createConversationViaAPIWithDetails, setPageFeatureFlag, withTempDir } from "./helpers";

// Turns the flag on in the worker's server for the test, which then opens a
// conversation whose one turn was "echo: water the basil".
async function withSuggestions(
  page: Page,
  request: APIRequestContext,
  run: (conversationId: string) => Promise<void>,
  options: { cwd?: string } = {},
) {
  const on = await request.post("/feature-flags", {
    data: { name: "next-prompt-suggestions", value: true },
  });
  expect(on.ok()).toBeTruthy();
  try {
    const { slug, conversationId } = await createConversationViaAPIWithDetails(
      request,
      "echo: water the basil",
      options,
    );
    await setPageFeatureFlag(page, "next-prompt-suggestions", true);
    await page.goto(`/c/${slug}`);
    await run(conversationId);
  } finally {
    const off = await request.delete("/feature-flags", {
      data: { name: "next-prompt-suggestions" },
    });
    expect(off.ok()).toBeTruthy();
  }
}

// The suggestion sits between the status and the text box.
async function expectBetween(suggestion: Locator, status: Locator, input: Locator) {
  const box = (await suggestion.boundingBox())!;
  const above = (await status.boundingBox())!;
  expect(box.y).toBeGreaterThanOrEqual(above.y + above.height);
  expect(box.y + box.height).toBeLessThanOrEqual((await input.boundingBox())!.y);
}

// After each turn, the suggested next message shows, labeled, above the
// empty composer: typing hides it, Tab or a click takes it, a new turn
// replaces it, and a new generation ends it.
test.describe("desktop", () => {
  test.use({ viewport: { width: 1280, height: 800 }, isMobile: false, hasTouch: false });
  test("the conversation suggests the next message", async ({ page, request }) => {
    await withSuggestions(page, request, async () => {
      const input = page.getByTestId("message-input");
      const suggestion = page.getByTestId("next-prompt-suggestion");
      await expect(suggestion).toHaveText(/^Suggestion\s*echo: More about water the basil/);
      await expectBetween(suggestion, page.locator(".status-bar-content"), input);

      await expect(input).toBeEnabled();
      await input.fill("x");
      await expect(suggestion).toBeHidden();
      await input.fill("");
      await expect(suggestion).toBeVisible();
      await input.press("Tab");
      await expect(input).toHaveValue("echo: More about water the basil");
      await expect(input).toBeFocused();
      await expect(suggestion).toBeHidden();
      await input.press("Enter");
      await expect(
        page.getByTestId("message").getByText("More about water the basil", { exact: true }),
      ).toBeVisible();
      await expect(suggestion).toContainText("echo: More about More about water the basil");

      await suggestion.click();
      await expect(input).toHaveValue("echo: More about More about water the basil");
      await expect(input).toBeFocused();
      await input.fill("/clear");
      await page.getByTestId("send-button").click();
      await expect(page.locator(".generation-divider")).toHaveCount(1);
      await expect(suggestion).toBeHidden();
    });
  });
  // Each conversation shows its own suggestion, and an archived one, with
  // no composer, shows none.
  test("each conversation has its own suggestion, and archived ones none", async ({
    page,
    request,
  }) => {
    await withSuggestions(page, request, async (conversationId) => {
      const suggestion = page.getByTestId("next-prompt-suggestion");
      await expect(suggestion).toContainText("echo: More about water the basil");

      const other = await createConversationViaAPIWithDetails(request, "echo: prune the rosemary");
      await page
        .locator(`.conversation-item[data-conversation-id="${other.conversationId}"]`)
        .click();
      await expect(suggestion).toContainText("echo: More about prune the rosemary");
      await page.locator(`.conversation-item[data-conversation-id="${conversationId}"]`).click();
      await expect(suggestion).toContainText("echo: More about water the basil");

      expect((await request.post(`/api/conversation/${conversationId}/archive`)).ok()).toBeTruthy();
      await page.reload();
      await expect(page.getByText("This conversation is archived.")).toBeVisible();
      await expect(suggestion).toBeHidden();
    });
  });
});

// A picked skill, like typed text, leaves the composer not empty.
test("a picked skill hides the suggestion", async ({ page, request }) => {
  await withTempDir("shelley-suggest-", async (cwd) => {
    const dir = join(cwd, ".skills", "draft-review");
    mkdirSync(dir, { recursive: true });
    writeFileSync(
      join(dir, "SKILL.md"),
      "---\nname: draft-review\ndescription: Review\n---\nRead.\n",
    );
    await withSuggestions(
      page,
      request,
      async () => {
        const suggestion = page.getByTestId("next-prompt-suggestion");
        await expect(suggestion).toBeVisible();
        await page.getByTestId("message-input").fill("/skills draft");
        await page.getByTestId("skill-picker").getByRole("option").first().click();
        await expect(page.getByTestId("skill-chip")).toContainText("draft-review");
        await expect(page.getByTestId("message-input")).toHaveValue("");
        await expect(suggestion).toBeHidden();
      },
      { cwd },
    );
  });
});

// Phones have no Tab: a tap on the suggestion takes it.
test.describe("phone", () => {
  test.use({ viewport: { width: 320, height: 640 }, isMobile: true, hasTouch: true });
  test("a phone taps to use the suggestion", async ({ page, request }) => {
    await withSuggestions(page, request, async () => {
      const input = page.getByTestId("message-input");
      const suggestion = page.getByTestId("next-prompt-suggestion");
      await expect(suggestion).toBeVisible();
      await expectBetween(suggestion, page.locator(".message-controls-status-slot"), input);
      await suggestion.tap();
      await expect(input).toHaveValue("echo: More about water the basil");
      await expect(suggestion).toBeHidden();
    });
  });
});
