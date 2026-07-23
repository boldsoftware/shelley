import { expect, test, type Page } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

test.use({
  viewport: { width: 1280, height: 720 },
  isMobile: false,
  hasTouch: false,
});

async function composerMetrics(page: Page, controlIds: string[]) {
  return page.evaluate((ids) => {
    function rect(testId: string) {
      const element = document.querySelector<HTMLElement>(`[data-testid="${testId}"]`);
      if (!element) throw new Error(`Missing ${testId}`);
      const bounds = element.getBoundingClientRect();
      return {
        top: bounds.top,
        bottom: bounds.bottom,
        height: bounds.height,
        centerY: (bounds.top + bounds.bottom) / 2,
      };
    }
    const form = document.querySelector<HTMLElement>(".message-input-form");
    if (!form) throw new Error("Missing message input form");
    const formBounds = form.getBoundingClientRect();
    return {
      input: rect("message-input"),
      controls: ids.map(rect),
      formHeight: formBounds.height,
    };
  }, controlIds);
}

function expectAligned(metrics: Awaited<ReturnType<typeof composerMetrics>>) {
  expect(Math.abs(metrics.formHeight - metrics.input.height)).toBeLessThan(0.5);
  for (const control of metrics.controls) {
    expect(Math.abs(control.centerY - metrics.input.centerY)).toBeLessThan(2.5);
  }
}

test("desktop new-conversation composer aligns the textarea and actions", async ({ page }) => {
  await page.goto("/new");

  const input = page.getByTestId("message-input");
  const attach = page.getByTestId("attach-button");
  const send = page.getByTestId("send-button");
  await expect(input).toBeVisible();
  await expect(attach).toBeVisible();
  await expect(send).toBeVisible();

  expectAligned(await composerMetrics(page, ["attach-button", "send-button"]));
});

test("desktop conversation composer aligns the textarea and split-send actions", async ({
  page,
  request,
}) => {
  const slug = await createConversationViaAPI(request, "composer alignment fixture");
  await page.goto(`/c/${slug}`);

  await expect(page.getByTestId("message-input")).toBeVisible();
  await expect(page.getByTestId("attach-button")).toBeVisible();
  await expect(page.getByTestId("send-button")).toBeVisible();
  await expect(page.getByTestId("send-options-button")).toBeVisible();

  expectAligned(
    await composerMetrics(page, ["attach-button", "send-button", "send-options-button"]),
  );
});
