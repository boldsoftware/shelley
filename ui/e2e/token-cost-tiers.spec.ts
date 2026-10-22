import { expect, test } from "@playwright/test";
import type { Message, Model } from "../src/types";

test.use({ viewport: { width: 1280, height: 900 } });

// Context-size pricing tiers (issue #322): a model like Claude Haiku 5.5 bills
// 5x more once a call's prompt exceeds 100k tokens. The cost graph must price
// each call at the tier its prompt lands in, not always at the base rate.
test("prices calls by the context tier their prompt lands in", async ({ page, request }) => {
  const model = "claude-haiku-5-5";
  const models: Model[] = [
    { id: "predictable", ready: true },
    {
      id: `${model}@integration`,
      api_model_name: model,
      base_url: "https://anthropic.test",
      ready: true,
    },
  ];
  await page.route("**/api/models", (route) => route.fulfill({ json: models }));
  // Base rates plus a context tier: above 100k prompt tokens, everything is 5x.
  await page.route("**/api/model-costs", (route) =>
    route.fulfill({
      json: {
        costs: {
          [model]: {
            input: 0.1,
            output: 0.5,
            cache_read: 0.01,
            cache_write: 0.125,
            tiers: [
              { context: 100000, input: 0.5, output: 2.5, cache_read: 0.05, cache_write: 0.625 },
            ],
          },
        },
      },
    }),
  );

  const generated = await request.post("/debug/loremipsum?json=1", {
    form: { size: "1", model: "predictable" },
  });
  expect(generated.ok()).toBeTruthy();
  const { conversation_id: id } = await generated.json();
  const response = await request.get(`/api/conversation/${id}`);
  expect(response.ok()).toBeTruthy();
  const body = await response.json();
  const agents = (body.messages as Message[]).filter((m) => m.type === "agent");
  expect(agents.length).toBeGreaterThanOrEqual(2);
  // Call 1: 60k prompt (< 100k) -> base rate. input 60k*$0.1/M=$0.006, output 10k*$0.5/M=$0.005.
  // Call 2: 150k prompt (> 100k) -> tier rate. input 150k*$0.5/M=$0.075, output 10k*$2.5/M=$0.025.
  // Total $0.111. At base rate throughout it would be only $0.031, so the
  // number itself proves the tier was applied.
  const promptsByCall = [60000, 150000];
  agents.forEach((message, index) => {
    // Only the first two calls carry cost; any extras the generator adds stay
    // at zero tokens so the total stays an exact, tier-revealing number.
    const input = promptsByCall[index] ?? 0;
    message.usage_data = JSON.stringify({
      model,
      url: "https://anthropic.test/v1/messages",
      input_tokens: input,
      cache_read_input_tokens: 0,
      cache_creation_input_tokens: 0,
      output_tokens: input > 0 ? 10000 : 0,
      cost_usd: 0,
    });
  });
  await page.route(`**/api/conversation/${id}`, (route) => route.fulfill({ json: body }));

  await page.goto("/new");
  await expect.poll(() => page.evaluate(() => window.__SHELLEY_INIT__?.models)).toEqual(models);
  await page.evaluate((slug) => {
    history.pushState({}, "", `/c/${slug}`);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, body.conversation.slug);

  await page.locator(".context-usage-label:visible").click();
  const popup = page.locator(".chat-context-popup");
  await expect(popup.locator(".token-cost-graph-svg")).toBeVisible();
  await expect(popup.getByTestId("token-cost-total")).toContainText("≈$0.111");
  // Calls straddle the threshold, so each band was served at two rates.
  await expect(
    popup.locator(".token-cost-legend-unit", { hasText: "mixed rates" }).first(),
  ).toBeVisible();
});
