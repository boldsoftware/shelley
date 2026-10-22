import { openModelPicker } from "./helpers";
import { expect, test } from "@playwright/test";

const customModel = (overrides: Record<string, unknown> = {}) => ({
  model_id: "custom-1",
  display_name: "Existing custom model",
  provider_type: "openai",
  endpoint: "https://api.openai.com/v1",
  has_api_key: true,
  model_name: "custom-model",
  max_tokens: 200000,
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
      const { api_key: _apiKey, ...fields } = updateBody!;
      currentModel = customModel(fields);
      await route.fulfill({ json: currentModel });
    });
    await page.route("**/api/custom-models-test", async (route) => {
      testBody = route.request().postDataJSON();
      await route.fulfill({ json: { success: true, message: "ok" } });
    });

    await page.goto("/new");
    await openModelPicker(page);
    await page.getByRole("button", { name: "Manage Models" }).click();
    await page.getByRole("button", { name: "Edit Model" }).click();

    const form = page.getByRole("dialog").last();
    const apiKey = form.locator('input[type="password"]');
    await expect(apiKey).toHaveValue("");
    await expect(apiKey).toHaveAttribute("placeholder", "Leave blank to keep the saved key");
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
    expect(updateBody?.api_key).toBe("");

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
    expect(testBody?.api_key).toBe("");
    expect(testBody?.model_id).toBe("custom-1");

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

    const replacementForm = page.getByRole("dialog").last();
    await expect(replacementForm.locator('input[type="password"]')).toHaveValue("");
    await replacementForm.locator('input[type="password"]').fill("replacement-key");
    await replacementForm.getByRole("button", { name: "Save" }).click();
    await expect.poll(() => updateBody?.api_key).toBe("replacement-key");
    await page.getByRole("button", { name: "Edit Model" }).click();
    await expect(page.getByRole("dialog").last().locator('input[type="password"]')).toHaveValue("");
  });
});
