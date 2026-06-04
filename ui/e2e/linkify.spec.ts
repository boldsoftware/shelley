import { test, expect } from "@playwright/test";

// Test that URLs in agent responses are properly linkified.
// With markdown enabled (default), agent messages render via Marked which
// auto-links URLs. With markdown disabled, linkifyText adds .text-link spans.

test("URLs in agent responses are linked (markdown mode)", async ({ page }) => {
  await page.goto('/new');
  await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

  await page.getByTestId("message-input").fill(
    "echo: Check https://example.com and https://test.com",
  );
  await page.getByTestId("send-button").click();

  await page.waitForSelector(".message-agent", { timeout: 30000 });

  // Markdown renderer auto-links URLs into <a> tags inside .markdown-content
  const agentLinks = page.locator(".message-agent .markdown-content a");
  await expect(agentLinks.first()).toBeVisible();
  await expect(agentLinks.first()).toHaveAttribute("href", "https://example.com");
  await expect(agentLinks.first()).toHaveAttribute("target", "_blank");
  await expect(agentLinks.first()).toHaveAttribute("rel", "noopener noreferrer");

  await expect(agentLinks.nth(1)).toBeVisible();
  await expect(agentLinks.nth(1)).toHaveAttribute("href", "https://test.com");
});

test("URLs are linkified in user messages too", async ({ page }) => {
  await page.goto('/new');
  await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

  await page.getByTestId("message-input").fill("echo: Visit https://example.com");
  await page.getByTestId("send-button").click();

  await page.waitForSelector(".message-user", { timeout: 30000 });

  // User messages always use linkifyText (never markdown)
  const userMessage = page.locator(".message-user").filter({
    hasText: "echo: Visit https://example.com",
  });
  await expect(userMessage).toContainText("echo: Visit https://example.com");

  const link = userMessage.locator("a.text-link");
  await expect(link).toHaveCount(1);
  await expect(link).toHaveAttribute("href", "https://example.com");
});
