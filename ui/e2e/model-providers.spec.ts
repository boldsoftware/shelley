import { test, expect } from "@playwright/test";
import { openModelPicker } from "./helpers";

test("shared add dialog, endpoint presets, and provider failure recovery", async ({ page }) => {
  const accountID = "account-1";
  const routeID = "provider:YWNjb3VudC0x:bW9kZWwtMQ";
  let providers = [
    {
      provider_id: accountID,
      provider_type: "openai",
      endpoint: "https://api.example.test/v1",
      display_name: "custom",
      has_api_key: true,
      model_count: 1,
    },
    {
      provider_id: "account-2",
      provider_type: "openai",
      endpoint: "https://other.example.test/v1",
      display_name: "Other",
      has_api_key: true,
      model_count: 0,
    },
  ];
  let accountRemoved = false;
  let providerInput: Record<string, unknown> | undefined;
  let singleInput: Record<string, unknown> | undefined;

  await page.route("**/api/model-providers/check", async (route) => {
    await route.fulfill({ json: [{ id: "gpt-6.1-sol", name: "Sol" }] });
  });

  await page.addInitScript((selectedModel) => {
    localStorage.setItem("shelley_selected_model", selectedModel);
  }, routeID);
  await page.route("**/api/model-providers", async (route) => {
    if (route.request().method() === "GET") {
      await route.fulfill({ json: providers });
      return;
    }
    if (route.request().method() === "POST") {
      providerInput = route.request().postDataJSON();
      await route.fulfill({ status: 502, contentType: "text/plain", body: "discovery failed\n" });
      return;
    }
    await route.continue();
  });
  await page.route(`**/api/model-providers/${accountID}/refresh`, async (route) => {
    await route.fulfill({ status: 502, contentType: "text/plain", body: "discovery failed\n" });
  });
  await page.route(`**/api/model-providers/${accountID}`, async (route) => {
    if (route.request().method() === "PUT") {
      providers = [{ ...providers[0], display_name: "Work edited", model_count: 2 }, providers[1]];
      await route.fulfill({ json: providers[0] });
      return;
    }
    if (route.request().method() === "DELETE") {
      providers = providers.filter((provider) => provider.provider_id !== accountID);
      accountRemoved = true;
      await route.fulfill({ status: 204 });
      return;
    }
    await route.continue();
  });
  await page.route("**/api/models", async (route) => {
    const models = accountRemoved
      ? []
      : [
          {
            id: routeID,
            display_name: "Work model",
            source: "custom",
            source_id: accountID,
            ready: true,
          },
        ];
    await route.fulfill({ json: models });
  });
  await page.route("**/api/custom-models", async (route) => {
    if (route.request().method() === "POST") {
      singleInput = route.request().postDataJSON();
      await route.fulfill({ json: { model_id: "single-test", ...singleInput } });
    } else {
      await route.fulfill({ json: [] });
    }
  });

  await page.goto("/new");
  await openModelPicker(page);
  const panel = page.locator(".model-picker-panel");
  await panel.getByRole("button", { name: "Manage Models" }).click();

  await expect(page.getByTestId("provider-connection").first()).toContainText("custom");
  await expect(page.locator(".models-cell-name")).toHaveText("Work model");
  await page.getByRole("button", { name: "+ Add Models", exact: true }).click();
  await expect(page.getByTestId("model-mode-provider")).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByTestId("provider-endpoint")).toHaveValue("https://api.anthropic.com/v1");
  await page.getByTestId("model-mode-single").click();
  const singleForm = page.getByRole("dialog").last().locator(".model-form:visible");
  await expect(singleForm.getByTestId("model-endpoint")).toHaveValue("https://api.anthropic.com/v1/messages");
  await singleForm.getByRole("button", { name: "OpenAI (Chat API)", exact: true }).click();
  await singleForm.getByRole("button", { name: "Endpoint", exact: true }).click();
  await page.getByRole("menuitem", { name: /^OpenRouter/ }).click();
  await expect(singleForm.getByTestId("model-endpoint")).toHaveValue("https://openrouter.ai/api/v1");
  await singleForm.getByTestId("model-endpoint").fill("https://single.example.test/v1");
  await singleForm.locator("input[placeholder^='Model name']").fill("single-model");
  await singleForm.locator(".form-group").filter({ hasText: "Display Name" }).locator("input").fill("Single model");
  await singleForm.locator(".form-group").filter({ hasText: "API Key" }).locator("input").fill("single-secret");
  await singleForm.getByRole("button", { name: "Add Model", exact: true }).click();
  await expect.poll(() => singleInput).toMatchObject({
    provider_type: "openai", endpoint: "https://single.example.test/v1", model_name: "single-model",
  });
  await page.getByRole("button", { name: "+ Add Models", exact: true }).click();
  await expect(page.getByTestId("model-mode-provider")).toHaveAttribute("aria-pressed", "true");
  const addForm = page.getByTestId("model-provider-form");
  await expect(addForm.getByTestId("provider-display-name")).toHaveAttribute("placeholder", "Optional, e.g. Work");
  await addForm.getByRole("button", { name: "OpenAI (Chat API)", exact: true }).click();
  await addForm.getByRole("button", { name: "Endpoint", exact: true }).click();
  await page.getByRole("menuitem", { name: /^Groq/ }).click();
  await expect(addForm.getByTestId("provider-endpoint")).toHaveValue("https://api.groq.com/openai/v1");
  await addForm.getByTestId("provider-endpoint").fill("https://custom.example.test/v1");
  await addForm.getByTestId("provider-display-name").fill("Failed add");
  await addForm.getByTestId("provider-api-key").fill("secret");
  await addForm.getByRole("button", { name: "Check models", exact: true }).click();
  await expect(addForm.getByRole("status")).toContainText("1 chat models: gpt-6.1-sol");
  expect(providerInput).toBeUndefined();
  await addForm.getByTestId("provider-api-key").fill("changed");
  await expect(addForm.getByRole("status")).toBeHidden();
  await addForm.getByTestId("provider-api-key").fill("secret");
  await addForm.getByRole("button", { name: "Connect", exact: true }).click();
  await expect(addForm.getByRole("alert")).toHaveText("Failed to add provider: discovery failed");
  expect(providerInput).toMatchObject({ provider_type: "openai", endpoint: "https://custom.example.test/v1" });
  await expect(page.getByTestId("provider-connection").first()).toContainText("custom");
  await addForm.getByRole("button", { name: "Cancel" }).click();

  await page.getByTestId(`refresh-provider-${accountID}`).click();
  await expect(page.locator(".models-error")).toContainText("discovery failed");
  await expect(page.getByTestId("provider-connection").first()).toContainText("custom");

  await page.getByTestId(`edit-provider-${accountID}`).click();
  const editForm = page.getByTestId("model-provider-form");
  await editForm.getByTestId("provider-display-name").fill("Work edited");
  await editForm.getByRole("button", { name: "Save" }).click();
  await expect(page.getByTestId("provider-connection").first()).toContainText("Work edited");
  await expect(page.getByTestId("provider-connection").first()).toContainText("(2)");

  await page.getByTestId(`remove-provider-${accountID}`).click();
  await page.getByTestId("confirm-remove-provider").click();
  await expect(page.getByTestId("provider-connection")).toHaveCount(1);
  await expect(page.getByText("This provider account was removed. Choose another model.")).toBeVisible();
  expect(await page.evaluate(() => localStorage.getItem("shelley_selected_model"))).toBe(routeID);
});

test("provider Connect cannot be dismissed while saving", async ({ page }) => {
  const saved = {
    provider_id: "pending-account", provider_type: "anthropic",
    endpoint: "https://api.anthropic.com/v1", display_name: "Pending account",
    has_api_key: true, model_count: 0,
  };
  let connected = false;
  let release!: () => void;
  const pending = new Promise<void>((resolve) => { release = resolve; });
  await page.route("**/api/models", (route) => route.fulfill({ json: [] }));
  await page.route("**/api/custom-models", (route) => route.fulfill({ json: [] }));
  await page.route("**/api/model-providers", async (route) => {
    if (route.request().method() === "POST") {
      await pending;
      connected = true;
      await route.fulfill({ status: 201, json: saved });
    } else {
      await route.fulfill({ json: connected ? [saved] : [] });
    }
  });

  await page.goto("/new");
  await openModelPicker(page);
  await page.getByRole("button", { name: "Manage Models", exact: true }).click();
  await page.getByRole("button", { name: "+ Add Models", exact: true }).click();
  const form = page.getByTestId("model-provider-form");
  await form.getByTestId("provider-api-key").fill("secret");
  await form.getByTestId("provider-display-name").fill("Pending account");
  try {
    await form.getByRole("button", { name: "Connect", exact: true }).click();
    await expect(form.getByRole("button", { name: "Saving…", exact: true })).toBeDisabled();
    await expect(page.getByTestId("model-mode-single")).toBeDisabled();
    await page.getByRole("dialog").last().getByRole("button", { name: "Close modal" }).click();
    await expect(form).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(form).toBeVisible();
  } finally {
    release();
  }
  await expect(form).toBeHidden();
  await expect(page.getByTestId("provider-connection")).toContainText("Pending account");
});

test("catalog is fetched once on connect and not on conversation switches", async ({ page, request }) => {
  const draftText = "Provider catalog navigation fixture";
  const draftResponse = await request.post("/api/conversations/draft", {
    data: { draft: draftText, model: "predictable" },
  });
  expect(draftResponse.ok()).toBeTruthy();
  const { conversation_id: draftID } = await draftResponse.json();
  const counts = { models: 0, providers: 0 };
  await page.route("**/api/models", async (route) => {
    counts.models++;
    await route.fulfill({ json: [{ id: "predictable", ready: true }] });
  });
  await page.route("**/api/model-providers", async (route) => {
    counts.providers++;
    await route.fulfill({ json: [] });
  });
  await page.goto("/new");
  await expect.poll(() => counts).toEqual({ models: 1, providers: 1 });
  await page.evaluate((id) => {
    window.history.pushState({}, "", `/c/${id}`);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, draftID);
  await expect(page.getByTestId("message-input")).toHaveValue(draftText);
  await page.evaluate(() => {
    window.history.pushState({}, "", "/new");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await expect(page.getByTestId("message-input")).toHaveValue("");
  expect(counts).toEqual({ models: 1, providers: 1 });
});
