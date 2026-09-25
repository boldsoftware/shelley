import { expect, test } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

test("expanded running tools show a live elapsed time and stop on completion", async ({
  page,
  request,
}) => {
  const slug = await createConversationViaAPI(request, "hello");
  await page.clock.install();
  await page.goto(`/c/${slug}`);
  const input = page.getByTestId("message-input");
  await expect(input).toBeVisible();

  await input.fill("bash: sleep 100");
  await page.getByTestId("send-button").click();
  const tool = page.locator(".bash-tool");
  await expect(tool).toHaveAttribute("data-testid", "tool-call-running", { timeout: 30000 });
  const elapsed = tool.getByTestId("tool-running-elapsed");
  await expect(elapsed).toHaveCount(0);

  await tool.locator(".bash-tool-header").click();
  await expect(elapsed).toHaveText(/Running for \d+s/);
  const initial = Number((await elapsed.textContent())?.match(/(\d+)s/)?.[1]);
  await page.clock.fastForward(5000);
  await expect
    .poll(async () => Number((await elapsed.textContent())?.match(/(\d+)s/)?.[1]))
    .toBeGreaterThanOrEqual(initial + 5);

  await tool.locator(".bash-tool-header").click();
  await expect(elapsed).toHaveCount(0);
  await page.clock.fastForward(3000);
  await tool.locator(".bash-tool-header").click();
  await expect
    .poll(async () => Number((await elapsed.textContent())?.match(/(\d+)s/)?.[1]))
    .toBeGreaterThanOrEqual(initial + 8);

  await page.locator(".status-stop-button").click();
  await expect(tool).toHaveAttribute("data-testid", "tool-call-completed", { timeout: 10000 });
  await expect(elapsed).toHaveCount(0);
  await tool.locator(".bash-tool-header").click();
  await expect(tool.locator(".bash-tool-time")).toHaveCount(0);
});

test("subagent tools and their running requests show elapsed time", async ({ page, request }) => {
  const slug = await createConversationViaAPI(request, "hello");
  await page.goto(`/c/${slug}`);
  const input = page.getByTestId("message-input");
  await expect(input).toBeVisible();

  await input.fill("subagent: helper bash: sleep 100");
  await page.getByTestId("send-button").click();
  const subagent = page
    .locator(".tool")
    .filter({ has: page.locator(".tool-name", { hasText: "subagent" }) });
  await expect(subagent).toHaveAttribute("data-testid", "tool-call-running", { timeout: 30000 });
  await subagent.locator(".tool-header").click();
  await expect(subagent.getByTestId("tool-running-elapsed")).toContainText("Running for");

  await page.getByTestId("subagent-live").click();
  const childTool = page.locator(".bash-tool");
  await expect(childTool).toHaveAttribute("data-testid", "tool-call-running", { timeout: 30000 });
  await childTool.locator(".bash-tool-header").click();
  await expect(childTool.getByTestId("tool-running-elapsed")).toContainText("Running for");

  await page.locator(".status-stop-button").click();
  await expect(childTool).toHaveAttribute("data-testid", "tool-call-completed", { timeout: 10000 });
});
