import { expect, test } from "@playwright/test";
import type { Message, Model } from "../src/types";

test.use({ viewport: { width: 1280, height: 900 } });

// Usage carries native model names and endpoints, not the picker's suffixed IDs.
// Mixed integrations can contribute to the same model's cost breakdown.
for (const width of [768, 1280]) {
  test(`hides only zero cache writes for ChatGPT subscription models (${width}px)`, async ({
    page,
    request,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    const cases = [
      { model: "gpt-5.6-luna", mode: "chatgpt", base: "https://subscription.test", writes: 0 },
      { model: "gpt-6-astra", mode: "chatgpt", base: "https://subscription.test", writes: 42 },
      { model: "gpt-5.6-sol", mode: "chatgpt", base: "https://subscription.test", writes: 0 },
      { model: "gpt-5.6-sol", mode: "managed", base: "https://managed.test", writes: 0 },
      { model: "gpt-5.6-terra", mode: "byok", base: "https://byok.test", writes: 0 },
      { model: "unknown-mode", mode: undefined, base: "https://unknown.test", writes: 0 },
      { model: "removed-model", mode: "chatgpt", base: "https://removed.test", writes: 0 },
      {
        model: "gpt-5.5",
        mode: "chatgpt",
        base: "https://subscription.test",
        writes: 0,
        url: "https://subscription.test.other/v1/responses",
      },
    ];
    const models: Model[] = [
      { id: "predictable", ready: true },
      ...cases
        .filter(({ model }) => model !== "removed-model")
        .map(({ model, mode, base }, index) => ({
          id: `${model}@integration-${index}`,
          api_model_name: model,
          base_url: base,
          mode,
          ready: true,
        })),
    ];
    await page.route("**/api/models", (route) => route.fulfill({ json: models }));
    await page.route("**/api/model-costs", (route) =>
      route.fulfill({
        json: {
          costs: Object.fromEntries(
            cases.map(({ model }) => [
              model,
              { input: 1, output: 2, cache_read: 0.1, cache_write: 1.25 },
            ]),
          ),
        },
      }),
    );

    const generated = await request.post("/debug/loremipsum?json=1", {
      form: { size: "4", model: "predictable" },
    });
    expect(generated.ok()).toBeTruthy();
    const { conversation_id: id } = await generated.json();
    const response = await request.get(`/api/conversation/${id}`);
    expect(response.ok()).toBeTruthy();
    const body = await response.json();
    const agents = (body.messages as Message[]).filter((message) => message.type === "agent");
    expect(agents).toHaveLength(cases.length);
    agents.forEach((message, index) => {
      const entry = cases[index];
      message.usage_data = JSON.stringify({
        model: entry.model,
        url: entry.url || `${entry.base}/v1/responses`,
        input_tokens: 1000,
        cache_read_input_tokens: 2000,
        cache_creation_input_tokens: entry.writes,
        output_tokens: 100,
        cost_usd: 0,
      });
    });
    await page.route(`**/api/conversation/${id}`, (route) => route.fulfill({ json: body }));

    // /new loads /api/models; client-side navigation keeps that catalog in the
    // status readout, exercising its propagation all the way to the cost graph.
    await page.goto("/new");
    await expect.poll(() => page.evaluate(() => window.__SHELLEY_INIT__?.models)).toEqual(models);
    await page.evaluate((slug) => {
      history.pushState({}, "", `/c/${slug}`);
      window.dispatchEvent(new PopStateEvent("popstate"));
    }, body.conversation.slug);
    await page.locator(".context-usage-label:visible").click();
    const popup = page.locator(".chat-context-popup");
    await expect(popup.locator(".token-cost-graph-svg")).toBeVisible();
    await expect(popup.getByTestId("token-cost-total")).toContainText("≈$0.011");

    const breakdown = () =>
      popup.getByRole("table", { name: "Spend by model" }).evaluate((legend) => {
        const result: Record<string, { label: string; tokens: string }[]> = {};
        let model = "";
        for (const row of legend.querySelectorAll(
          ".token-cost-model-row, .token-cost-legend-row",
        )) {
          if (row.matches(".token-cost-model-row")) {
            model = row.querySelector(".token-cost-model-name")!.textContent!.trim();
            result[model] = [];
          } else if (row.matches(".token-cost-legend-row")) {
            result[model].push({
              label: row.querySelector(".token-cost-legend-label")!.textContent!.trim(),
              tokens: row.querySelector(".token-cost-legend-tokens")!.textContent!.trim(),
            });
          }
        }
        return result;
      });
    const rows = (writes: number | null, calls = 1) => [
      { label: "Output", tokens: String(100 * calls) },
      { label: "Input", tokens: `${calls}k` },
      ...(writes === null ? [] : [{ label: "Cache write", tokens: String(writes) }]),
      { label: "Cache read", tokens: `${2 * calls}k` },
    ];
    await expect.poll(breakdown).toMatchObject({
      "gpt-5.6-luna": rows(null),
      "gpt-6-astra": rows(42),
      "gpt-5.6-sol": rows(0, 2),
      "gpt-5.6-terra": rows(0),
      "unknown-mode": rows(0),
      "removed-model": rows(0),
      "gpt-5.5": rows(0),
    });
  });
}
