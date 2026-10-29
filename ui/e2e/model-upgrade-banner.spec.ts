import { test, expect, type Page } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

const next = {
  id: "predictable-next",
  display_name: "Predictable Next",
  ready: true,
  supports_images: true,
  supports_reasoning: false,
};
const upgrade = { to: next.id, kind: "version", line: "Predictable" };

// The e2e server cannot run the fake newer model, so answer the settings POST
// as if the switch landed; the offer waits for that.
async function landSwitch(page: Page) {
  await page.route(/\/api\/conversation\/[^/]+\/settings$/, async (route) => {
    if (route.request().method() !== "POST") return route.continue();
    const current = await (await route.fetch({ method: "GET", postData: undefined })).json();
    await route.fulfill({ json: { ...current, ...route.request().postDataJSON() } });
  });
}

type ModelEntry = { id: string; upgrade?: typeof upgrade };

// The e2e server only has the predictable model, which has no upgrade link,
// so give it one in both places the page reads its model list from.
async function offerUpgrade(page: Page, state: { dismissed: boolean }) {
  const withUpgrade = (models: ModelEntry[]) => [
    next,
    ...models.map((m) => (m.id === "predictable" && !state.dismissed ? { ...m, upgrade } : m)),
  ];
  await page.route("**/api/models", async (route) => {
    await route.fulfill({ json: withUpgrade(await (await route.fetch()).json()) });
  });
  await page.route(
    (url) => url.pathname === "/new" || /^\/c\/[^/]+$/.test(url.pathname),
    async (route) => {
      if (route.request().resourceType() !== "document") return route.continue();
      const html = await (await route.fetch()).text();
      const [json, start, end] = modelsInHtml(html);
      await route.fulfill({
        body:
          html.slice(0, start) + JSON.stringify(withUpgrade(JSON.parse(json))) + html.slice(end),
        contentType: "text/html",
      });
    },
  );
}

// The models array sits inside a JSON blob in the page, and its entries carry
// arrays of their own, so find it by balancing brackets.
function modelsInHtml(html: string): [string, number, number] {
  const key = '"models":[';
  const at = html.indexOf(key);
  if (at === -1) throw new Error("no models in init data");
  const start = at + key.length - 1;
  let depth = 0;
  for (let i = start; i < html.length; i++) {
    if (html[i] === "[") depth++;
    else if (html[i] === "]" && --depth === 0) return [html.slice(start, i + 1), start, i + 1];
  }
  throw new Error("unterminated models array in init data");
}

test.describe("model upgrade banner", () => {
  test("new chats show the upgrade before sending and Switch updates the composer", async ({
    page,
  }) => {
    await offerUpgrade(page, { dismissed: false });
    await page.addInitScript(() => localStorage.setItem("shelley_selected_model", "predictable"));
    await page.goto("/new");

    const banner = page.getByTestId("model-upgrade-banner");
    await expect(banner).toContainText("Newer version of Predictable available");
    await expect(banner).toContainText("predictable → Predictable Next");
    await page.getByTestId("model-upgrade-action").click();
    await expect(banner).toHaveCount(0);
    await expect(page.locator(".status-field-model .model-picker")).toContainText("Predictable Next");
    expect(await page.evaluate(() => localStorage.getItem("shelley_selected_model"))).toBe(next.id);
    await expect(page.getByTestId("model-upgrade-all-banner")).toHaveCount(0);
  });

  test("unsent drafts show the upgrade and Switch saves the model without sending", async ({
    page,
    request,
  }) => {
    await offerUpgrade(page, { dismissed: false });
    const response = await request.post("/api/conversations/draft", {
      data: { model: "predictable", draft: "Not sent yet" },
    });
    expect(response.ok()).toBeTruthy();
    const draft = await response.json();
    const switched = page.waitForRequest(
      (r) =>
        r.method() === "PUT" &&
        r.url().endsWith(`/conversation/${draft.conversation_id}/draft`) &&
        r.postDataJSON().model === next.id,
    );
    await page.route(`**/api/conversation/${draft.conversation_id}/draft`, (route) =>
      route.fulfill({ json: { ...draft, ...route.request().postDataJSON() } }),
    );
    await page.goto(`/c/${draft.slug}`);

    await expect(page.getByTestId("model-upgrade-banner")).toContainText(
      "Newer version of Predictable available",
    );
    await page.getByTestId("model-upgrade-action").click();
    expect((await switched).postDataJSON()).toEqual({ model: next.id });
    await expect(page.getByTestId("model-upgrade-banner")).toHaveCount(0);
    await expect(page.getByTestId("message-input")).toHaveValue("Not sent yet");
  });

  test("Switch sends the newer model", async ({ page, request }) => {
    await offerUpgrade(page, { dismissed: false });
    const slug = await createConversationViaAPI(request, "echo: upgrade me");
    const switched = page.waitForRequest(
      (r) => r.method() === "POST" && /\/conversation\/[^/]+\/settings$/.test(r.url()),
    );
    await page.goto(`/c/${slug}`);

    const banner = page.getByTestId("model-upgrade-banner");
    await expect(banner).toContainText("Newer version of Predictable available");
    await expect(banner).toContainText("predictable → Predictable Next");
    await page.getByTestId("model-upgrade-action").click();
    // The e2e server only has the predictable model, so the switch itself
    // cannot land here; this pins the target the click sends. Dismissal below
    // covers the banner coming back down.
    expect((await switched).postDataJSON()).toMatchObject({ model: next.id });
  });

  test("offers once to repeat the switch on other chats", async ({ page, request }) => {
    await offerUpgrade(page, { dismissed: false });
    await createConversationViaAPI(request, "echo: still on the old model");
    const slug = await createConversationViaAPI(request, "echo: upgrade them all");
    await landSwitch(page);
    const applied = page.waitForRequest("**/api/model-upgrade/apply-all");
    await page.route("**/api/model-upgrade/apply-all", (route) =>
      route.fulfill({ json: { upgraded: 2, skipped: 1 } }),
    );
    await page.goto(`/c/${slug}`);

    const offer = page.getByTestId("model-upgrade-all-banner");
    await expect(offer).toHaveCount(0);
    await page.getByTestId("model-upgrade-action").click();
    await expect(offer).toContainText("Switch your other");
    await expect(offer).toContainText("Predictable Next");
    await page.getByTestId("model-upgrade-all-action").click();
    // The exact switch just made travels with the request.
    expect((await applied).postDataJSON()).toEqual({ from: "predictable", to: next.id });
    await expect(offer).toHaveCount(0);
    // The count is said, not left to guesswork.
    await expect(page.getByTestId("model-upgrade-all-result-banner")).toContainText(
      "Switched 2 chats from",
    );
    await expect(page.getByTestId("model-upgrade-all-result-banner")).toContainText(
      "Skipped 1 chat still running",
    );
  });

  test("dismissing the offer records the answer", async ({ page, request }) => {
    await offerUpgrade(page, { dismissed: false });
    await createConversationViaAPI(request, "echo: still on the old model");
    const slug = await createConversationViaAPI(request, "echo: no thanks");
    await landSwitch(page);
    const dismissed = page.waitForRequest("**/api/model-upgrade/prompt-dismissed");
    await page.route("**/api/model-upgrade/prompt-dismissed", (route) =>
      route.fulfill({ status: 204 }),
    );
    await page.goto(`/c/${slug}`);

    await page.getByTestId("model-upgrade-action").click();
    await page.getByTestId("model-upgrade-all-dismiss").click();
    await dismissed;
    await expect(page.getByTestId("model-upgrade-all-banner")).toHaveCount(0);
  });

  test("dismissing records the target and hides the banner", async ({ page, request }) => {
    const state = { dismissed: false };
    await offerUpgrade(page, state);
    const slug = await createConversationViaAPI(request, "echo: dismiss me");
    const dismissed = page.waitForRequest("**/api/model-upgrade/dismiss");
    await page.route("**/api/model-upgrade/dismiss", async (route) => {
      state.dismissed = true;
      await route.fulfill({ status: 204 });
    });
    await page.goto(`/c/${slug}`);

    await expect(page.getByTestId("model-upgrade-banner")).toBeVisible();
    await page.getByTestId("model-upgrade-dismiss").click();
    expect((await dismissed).postDataJSON()).toEqual({ to: next.id });
    await expect(page.getByTestId("model-upgrade-banner")).toHaveCount(0);
  });
});
