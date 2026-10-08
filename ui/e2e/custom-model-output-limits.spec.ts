import { expect, test } from "@playwright/test";

const customModel = (overrides: Record<string, unknown> = {}) => ({
  model_id: "custom-1",
  display_name: "Existing custom model",
  provider_type: "openai",
  endpoint: "https://api.openai.com/v1",
  api_key: "test-key",
  model_name: "custom-model",
  max_tokens: 200000,
  tags: "",
  reasoning_effort: "",
  reasoning_support: "auto",
  reasoning_map: "",
  supports_reasoning: false,
  image_support: "auto",
  supports_images: true,
  ...overrides,
});

test.describe("custom model output token limit", () => {
  test("preserves, tests, saves, and blanks max output tokens", async ({ page }) => {
    let currentModel = customModel();
    let updateBody: Record<string, unknown> | undefined;
    let testBody: Record<string, unknown> | undefined;

    await page.route("**/api/models", (route) => route.fulfill({ json: [] }));
    await page.route("**/api/custom-models", async (route) => {
      if (route.request().method() === "GET") {
        await route.fulfill({ json: [currentModel] });
      } else {
        await route.continue();
      }
    });
    await page.route("**/api/custom-models/custom-1", async (route) => {
      updateBody = route.request().postDataJSON();
      currentModel = customModel(updateBody);
      await route.fulfill({ json: currentModel });
    });
    await page.route("**/api/custom-models-test", async (route) => {
      testBody = route.request().postDataJSON();
      await route.fulfill({ json: { success: true, message: "ok" } });
    });

    await page.goto("/new");
    await page.locator(".model-picker").click();
    await page.getByRole("button", { name: "Manage Models" }).click();
    await page.getByRole("button", { name: "Edit Model" }).click();

    const form = page.getByRole("dialog").last();
    const maxTokens = form
      .locator(".form-group")
      .filter({ hasText: "Max output tokens" })
      .locator("input");
    await expect(maxTokens).toHaveValue("200000");

    const displayName = form
      .locator(".form-group")
      .filter({ hasText: "Display Name" })
      .locator("input");
    await displayName.fill("Renamed custom model");
    await form.getByRole("button", { name: "Save" }).click();
    await expect.poll(() => updateBody?.max_tokens).toBe(200000);

    await page.getByRole("button", { name: "Edit Model" }).click();
    const reopenedForm = page.getByRole("dialog").last();
    const reopenedMaxTokens = reopenedForm
      .locator(".form-group")
      .filter({ hasText: "Max output tokens" })
      .locator("input");
    await expect(reopenedMaxTokens).toHaveValue("200000");
    await reopenedMaxTokens.fill("123456");

    await reopenedForm.getByRole("button", { name: "Test" }).click();
    await expect.poll(() => testBody?.max_tokens).toBe(123456);

    await reopenedForm.getByRole("button", { name: "Save" }).click();
    await expect.poll(() => updateBody?.max_tokens).toBe(123456);

    // Blank saves as 0 (provider default) and reopens blank, with the
    // placeholder naming what blank resolves to for this provider.
    await page.getByRole("button", { name: "Edit Model" }).click();
    const blankForm = page.getByRole("dialog").last();
    const blankMaxTokens = blankForm
      .locator(".form-group")
      .filter({ hasText: "Max output tokens" })
      .locator("input");
    await expect(blankMaxTokens).toHaveValue("123456");
    await blankMaxTokens.fill("");
    await expect(blankMaxTokens).toHaveAttribute("placeholder", "Model's published limit");
    await blankForm.getByRole("button", { name: "Save" }).click();
    await expect.poll(() => updateBody?.max_tokens).toBe(0);

    await page.getByRole("button", { name: "Edit Model" }).click();
    const zeroMaxTokens = page
      .getByRole("dialog")
      .last()
      .locator(".form-group")
      .filter({ hasText: "Max output tokens" })
      .locator("input");
    await expect(zeroMaxTokens).toHaveValue("");
    await expect(zeroMaxTokens).toHaveAttribute("placeholder", "Model's published limit");
  });
});
