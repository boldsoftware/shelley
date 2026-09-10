import { expect, test } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

test("groups model and system prompt into one context card", async ({ page, request }) => {
  await page.setViewportSize({ width: 1000, height: 800 });
  const conversation = await createConversationViaAPIWithDetails(request, "echo: context card");

  await page.goto(`/c/${conversation.slug}`);
  await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

  const card = page.getByTestId("generation-context");
  await expect(card).toBeVisible();
  await expect(card.locator(":scope > .model-bar")).toBeVisible();
  await expect(card.locator(":scope > .system-prompt-view")).toBeVisible();
  const modelSummary = card.locator(".model-bar-summary");
  const promptButton = card.getByRole("button", {
    name: /System Prompt:\s*\d+ tools?\s*,?\s*\d+ skills? Expand/,
  });
  await expect(promptButton).toBeVisible();
  await expect(modelSummary).toContainText("Model:");
  await expect(modelSummary).not.toContainText("Reasoning");
  await expect(promptButton).toContainText(/System Prompt:\s*\d+ tools?,\s*\d+ skills?/);
  expect(
    await promptButton.evaluate((element) => ({
      boxShadow: getComputedStyle(element).boxShadow,
      borderLeftWidth: getComputedStyle(element).borderLeftWidth,
    })),
  ).toEqual({ boxShadow: "none", borderLeftWidth: "0px" });

  await modelSummary
    .locator(".model-bar-name")
    .first()
    .evaluate((element) => {
      element.textContent = "a-very-long-custom-model-name-".repeat(12);
    });
  const [cardRect, promptRect] = await Promise.all([
    card.evaluate((element) => element.getBoundingClientRect().toJSON()),
    promptButton.evaluate((element) => element.getBoundingClientRect().toJSON()),
  ]);
  expect(promptRect.width).toBeGreaterThan(200);
  expect(promptRect.right).toBeLessThanOrEqual(cardRect.right + 1);

  const lineRects = await Promise.all([
    modelSummary.evaluate((element) => {
      const rect = element.getBoundingClientRect();
      return { top: rect.top, bottom: rect.bottom };
    }),
    promptButton.evaluate((element) => {
      const rect = element.getBoundingClientRect();
      return { top: rect.top, bottom: rect.bottom };
    }),
  ]);
  expect(Math.abs(lineRects[0].top - lineRects[1].top)).toBeLessThan(1);
  expect(Math.abs(lineRects[0].bottom - lineRects[1].bottom)).toBeLessThan(1);

  await page.setViewportSize({ width: 540, height: 800 });
  await expect(promptButton).toBeVisible();

  const skillCount = Number((await promptButton.textContent())?.match(/(\d+) skills?/)?.[1]);
  const toolCount = Number((await promptButton.textContent())?.match(/(\d+) tools?/)?.[1]);
  expect(skillCount).toBeGreaterThan(0);
  expect(toolCount).toBeGreaterThan(0);
  await promptButton.click();
  await expect(
    card.getByRole("button", {
      name: /System Prompt:\s*\d+ tools?\s*,?\s*\d+ skills? Collapse/,
    }),
  ).toBeVisible();
  await expect(card.locator(".system-prompt-content")).toBeVisible();
  await expect(card.locator(".system-prompt-skill-item")).toHaveCount(skillCount);
  await expect(card.locator(".system-prompt-tool-item")).toHaveCount(toolCount);

  const skillCard = card.locator(".system-prompt-skill-item").first();
  await skillCard.locator(".system-prompt-card-summary").click();
  await expect(skillCard.locator(".system-prompt-card-detail")).toBeVisible();
  await expect(skillCard.locator(".system-prompt-card-detail")).toContainText("Source");
  await expect(skillCard.locator(".system-prompt-card-detail code").first()).toContainText(
    "SKILL.md",
  );

  const toolCard = card.locator(".system-prompt-tool-item").first();
  await toolCard.locator(".system-prompt-card-summary").click();
  await expect(toolCard.locator(".system-prompt-card-detail")).toBeVisible();
  await expect(toolCard.locator(".system-prompt-card-detail")).toContainText("Source");
  await expect(toolCard.locator(".system-prompt-card-detail code").first()).toContainText(
    "claudetool/",
  );

  const childSurfaces = await card
    .locator(":scope > .model-bar, :scope > .system-prompt-view")
    .evaluateAll((elements) =>
      elements.map((element) => {
        const style = getComputedStyle(element);
        return {
          backgroundColor: style.backgroundColor,
          borderTopWidth: style.borderTopWidth,
          borderRightWidth: style.borderRightWidth,
          borderBottomWidth: style.borderBottomWidth,
          borderLeftWidth: style.borderLeftWidth,
        };
      }),
    );
  expect(childSurfaces).toEqual([
    {
      backgroundColor: "rgba(0, 0, 0, 0)",
      borderTopWidth: "0px",
      borderRightWidth: "0px",
      borderBottomWidth: "0px",
      borderLeftWidth: "0px",
    },
    {
      backgroundColor: "rgba(0, 0, 0, 0)",
      borderTopWidth: "0px",
      borderRightWidth: "0px",
      borderBottomWidth: "0px",
      borderLeftWidth: "0px",
    },
  ]);
});
