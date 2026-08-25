import { expect, test } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

test("Bash commands are Shiki-tokenized without changing output rendering", async ({
  page,
  request,
}) => {
  const command = "if true; then echo bash-output-plain; fi";
  const slug = await createConversationViaAPI(request, `bash: ${command}`);
  await page.goto(`/c/${slug}`);
  await page.waitForLoadState("domcontentloaded");

  const bashTool = page.locator(".bash-tool").filter({ hasText: command }).first();
  const summary = bashTool.locator(".bash-tool-command");
  await expect(summary).toHaveText(command, { timeout: 30000 });
  await expect(summary).toHaveAttribute("title", command);
  await expect(summary.locator(".shelley-code-token").first()).toBeAttached();

  const summaryTokens = summary.locator(".shelley-code-token");
  const tokenCount = await summaryTokens.count();
  const lightColor = await summaryTokens.first().evaluate((token) => getComputedStyle(token).color);
  const tokenHtml = await summary.innerHTML();
  await page.locator("html").evaluate((root) => root.classList.add("dark"));
  await expect
    .poll(() => summaryTokens.first().evaluate((token) => getComputedStyle(token).color))
    .not.toBe(lightColor);
  expect(await summaryTokens.count()).toBe(tokenCount);
  expect(await summary.innerHTML()).toBe(tokenHtml);

  await bashTool.locator(".bash-tool-header").click();
  const details = bashTool.locator(".bash-tool-details");
  await expect(details).toBeVisible();
  const expandedCommand = details.locator(".bash-tool-code").filter({ hasText: command });
  await expect(expandedCommand).toHaveText(command);
  await expect(expandedCommand.locator(".shelley-code-token").first()).toBeAttached();

  const output = details.locator(".bash-tool-code").last();
  await expect(output).toHaveText("bash-output-plain\n");
  await expect(output.locator(".shelley-code-token")).toHaveCount(0);
  expect(await output.innerHTML()).not.toContain("shelley-code-token");
});
