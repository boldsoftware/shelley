import { test, expect } from "@playwright/test";
import { openModelPicker } from "./helpers";

test("Gemini provider discovers and connects using its native endpoint", async ({ page }) => {
  const saved = {
    provider_id: "google-account",
    provider_type: "gemini",
    endpoint: "https://generativelanguage.googleapis.com/v1beta",
    display_name: "Google",
    has_api_key: true,
    model_count: 1,
  };
  let connected = false;
  let checkInput: Record<string, unknown> | undefined;
  let connectInput: Record<string, unknown> | undefined;
  await page.route("**/api/custom-models", (route) => route.fulfill({ json: [] }));
  await page.route("**/api/models", (route) =>
    route.fulfill({
      json: connected
        ? [
            {
              id: "provider:Z29vZ2xlLWFjY291bnQ:Z2VtaW5pLTMuOC1mbGFzaA",
              ready: true,
              source_id: saved.provider_id,
              source: saved.display_name,
              api_model_name: "gemini-3.8-flash",
              api_type: "gemini",
              base_url: saved.endpoint,
            },
          ]
        : [],
    }),
  );
  await page.route("**/api/model-providers/check", async (route) => {
    checkInput = route.request().postDataJSON();
    await route.fulfill({ json: [{ id: "gemini-3.8-flash", name: "Gemini Flash" }] });
  });
  await page.route("**/api/model-providers", async (route) => {
    if (route.request().method() === "POST") {
      connectInput = route.request().postDataJSON();
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
  await form.getByRole("button", { name: "Google Gemini", exact: true }).click();
  await expect(form.getByTestId("provider-endpoint")).toHaveValue(saved.endpoint);
  await form.getByTestId("provider-api-key").fill("gemini-secret");
  await form.getByTestId("provider-display-name").fill(saved.display_name);
  await form.getByRole("button", { name: "Check models", exact: true }).click();
  await expect(form.getByRole("status")).toContainText("1 chat models: gemini-3.8-flash");
  expect(checkInput).toEqual({
    provider_type: "gemini",
    endpoint: saved.endpoint,
    api_key: "gemini-secret",
    display_name: saved.display_name,
  });
  expect(connected).toBe(false);
  await form.getByRole("button", { name: "Connect", exact: true }).click();
  await expect(form).toBeHidden();
  expect(connectInput).toEqual(checkInput);
  await expect(page.getByTestId("provider-connection")).toContainText("Google");
  await expect(page.getByTestId("model-provider-list")).toContainText("Gemini");
});

