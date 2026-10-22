import { expect, test, type Page, type APIRequestContext } from "@playwright/test";
import type { Message } from "../src/types";

test.use({ viewport: { width: 1280, height: 900 } });

const price = { input: 2, output: 8, cache_read: 0.2, cache_write: 2.5 };
const reviewer = {
  model: "review-model",
  url: "https://review.test",
  llm_calls: 4,
  input_tokens: 2_000_000,
  output_tokens: 250_000,
  cache_read_input_tokens: 5_000_000,
  cache_creation_input_tokens: 200_000,
  estimated_usd: 7.5,
  reported_usd: 999,
  cost: price,
};
const unpriced = {
  model: "unpriced-model",
  url: "https://unpriced.test",
  llm_calls: 1,
  input_tokens: 3000,
  output_tokens: 500,
  cache_read_input_tokens: 0,
  cache_creation_input_tokens: 0,
  estimated_usd: 0,
  reported_usd: 0.75,
  cost: null,
};
const usage = {
  llm_calls: 5,
  estimated_usd: 7.5,
  reported_usd: 999.75,
  unpriced_reported_usd: 0.75,
  unpriced_models: [unpriced.model],
  unpriced_calls: 1,
  per_model: [reviewer, unpriced],
};

async function openSpend(
  page: Page,
  request: APIRequestContext,
  { direct = true, pricingFailed = false }: { direct?: boolean; pricingFailed?: boolean } = {},
) {
  await page.route("**/api/model-costs", (route) =>
    route.fulfill(pricingFailed ? { status: 503 } : { json: { costs: { "main-model": price } } }),
  );
  const generated = await request.post("/debug/loremipsum?json=1", {
    form: { size: "1", model: "predictable" },
  });
  expect(generated.ok()).toBeTruthy();
  const { conversation_id: id } = await generated.json();
  const response = await request.get(`/api/conversation/${id}`);
  expect(response.ok()).toBeTruthy();
  const body = await response.json();
  const agents = (body.messages as Message[]).filter((message) => message.type === "agent");
  for (const message of body.messages as Message[]) {
    message.usage_data = null;
    message.other_usage_data = null;
  }
  if (direct) {
    agents[0].usage_data = JSON.stringify({
      model: "main-model",
      input_tokens: 1_000_000,
      output_tokens: 100_000,
      cache_read_input_tokens: 2_000_000,
      cache_creation_input_tokens: 100_000,
      cost_usd: 999,
    });
    agents[0].other_usage_data = JSON.stringify([
      { purpose: "compaction", model: "main-model", input_tokens: 100_000, output_tokens: 10_000 },
    ]);
  }
  await page.route(`**/api/conversation/${id}`, (route) => route.fulfill({ json: body }));
  await page.goto(`/c/${body.conversation.slug}`);
  await page.locator(".context-usage-label:visible").click();
  return page.locator(".chat-context-popup");
}

for (const width of [768, 1280]) {
  test(`per-model main and sub-agent spend with subtotals (${width}px)`, async ({
    page,
    request,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.route("**/api/conversation/*/subagent-usage", (route) =>
      route.fulfill({ json: usage }),
    );
    const popup = await openSpend(page, request);
    const table = popup.getByRole("table", { name: "Spend by model" });
    await expect(table).toBeVisible();
    await expect(table.getByRole("columnheader")).toHaveText([
      "Model / tokens",
      "Main conversation",
      "Sub-agents",
    ]);
    await expect(table.getByTestId("conversation-cost-subtotal")).toContainText("$3.73");
    await expect(table.getByTestId("subagent-cost-row")).toContainText("$8.25");
    await expect(table.getByTestId("token-cost-total")).toContainText("≈$11.98");
    const models = table.locator(".token-cost-model-breakdown");
    await expect(models).toHaveCount(3);
    await expect(models.nth(0).locator(".token-cost-model-row")).toHaveText(
      /main-model.*\$3.45.*—/,
    );
    await expect(models.nth(1).locator(".token-cost-model-row")).toHaveText(
      /review-model.*—.*\$7.50/,
    );
    await expect(
      models.nth(1).locator('.token-cost-legend-row [data-scope="subagents"]'),
    ).toHaveText([
      /250k\s*@ \$8\/M\s*\$2.00/,
      /2.0M\s*@ \$2\/M\s*\$4.00/,
      /200k\s*@ \$2.50\/M\s*\$0.500/,
      /5.0M\s*@ \$0.20\/M\s*\$1.00/,
    ]);
    await expect(models.nth(2)).toContainText("unpriced-model");
    await expect(models.nth(2)).toContainText("$0.750 reported");
    await expect(
      models.nth(2).locator(".token-cost-legend-row .token-cost-legend-unit"),
    ).toHaveCount(0);
    await expect(popup).toContainText("1 call has no model pricing");
    await expect(popup).toContainText(
      "Sub-agent model totals include nested sub-agents and indirect usage.",
    );
    await expect(table).toContainText("Other (indirect)");
    await expect(table).toContainText("in model totals");
    const headers = table.getByRole("columnheader");
    const mainBox = (await headers.nth(1).boundingBox())!;
    const subBox = (await headers.nth(2).boundingBox())!;
    expect(mainBox.y).toBe(subBox.y);
    expect(subBox.x).toBeGreaterThanOrEqual(mainBox.x + mainBox.width);
    const popupBox = (await popup.boundingBox())!;
    expect(popupBox.x).toBeGreaterThanOrEqual(0);
    expect(popupBox.x + popupBox.width).toBeLessThanOrEqual(width);
    expect(await popup.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
  });
}

test("sub-agent models and totals without direct usage", async ({ page, request }) => {
  await page.route("**/api/conversation/*/subagent-usage", (route) =>
    route.fulfill({ json: usage }),
  );
  const popup = await openSpend(page, request, { direct: false });
  await expect(popup).toContainText("No direct usage data yet.");
  await expect(popup.getByRole("table", { name: "Spend by model" })).toContainText("review-model");
  await expect(popup.getByTestId("token-cost-total")).toContainText("≈$8.25");
});

test("unpriced sub-agent usage is not presented as free", async ({ page, request }) => {
  await page.route("**/api/conversation/*/subagent-usage", (route) =>
    route.fulfill({
      json: {
        ...usage,
        llm_calls: 1,
        estimated_usd: 0,
        reported_usd: 0,
        unpriced_reported_usd: 0,
        per_model: [{ ...unpriced, reported_usd: 0 }],
      },
    }),
  );
  const popup = await openSpend(page, request, { direct: false });
  const subagents = popup.getByRole("table", { name: "Spend by model" });
  await expect(subagents.locator(".token-cost-model-breakdown")).toContainText("no pricing");
  await expect(subagents.getByTestId("subagent-cost-row")).toContainText("no pricing");
  await expect(popup).toContainText("total may be incomplete");
  await expect(popup.getByTestId("token-cost-total")).toBeHidden();
});

test("keeps conversations without sub-agents compact", async ({ page, request }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  const popup = await openSpend(page, request);
  await expect(popup.getByTestId("token-cost-total")).toHaveText("Total≈$3.73");
  await expect(popup.getByRole("columnheader", { name: "Sub-agents", exact: true })).toHaveCount(0);
  expect((await popup.boundingBox())!.width).toBe(440);
});

test("shows a zero total when sub-agent pricing is known and free", async ({ page, request }) => {
  await page.route("**/api/conversation/*/subagent-usage", (route) =>
    route.fulfill({
      json: {
        ...usage,
        llm_calls: 4,
        estimated_usd: 0,
        reported_usd: 0,
        unpriced_reported_usd: 0,
        unpriced_calls: 0,
        unpriced_models: [],
        per_model: [
          {
            ...reviewer,
            estimated_usd: 0,
            reported_usd: 0,
            cost: { input: 0, output: 0, cache_read: 0, cache_write: 0 },
          },
        ],
      },
    }),
  );
  const popup = await openSpend(page, request, { direct: false });
  await expect(popup.getByTestId("subagent-cost-row")).toHaveText("$0");
  await expect(popup.getByTestId("token-cost-total")).toHaveText("Total≈$0");
  await expect(popup).not.toContainText("no pricing");
});

test("warns when sub-agent details cannot be fetched", async ({ page, request }) => {
  await page.route("**/api/conversation/*/subagent-usage", (route) =>
    route.fulfill({ status: 500, body: "unavailable" }),
  );
  const popup = await openSpend(page, request);
  await expect(popup.getByTestId("token-cost-total")).toHaveText("Total≈$3.73");
  await expect(popup).toContainText("Subagent cost unavailable; total may be incomplete.");
  await expect(popup).not.toContainText("Loading total");
});

test("merges endpoint-specific prices into one model row without inventing a shared rate", async ({
  page,
  request,
}) => {
  await page.route("**/api/conversation/*/subagent-usage", (route) =>
    route.fulfill({
      json: {
        ...usage,
        llm_calls: 8,
        estimated_usd: 19,
        reported_usd: 1998,
        unpriced_reported_usd: 0,
        unpriced_models: [],
        unpriced_calls: 0,
        per_model: [
          reviewer,
          {
            ...reviewer,
            url: "https://second.test/another-provider",
            estimated_usd: 11.5,
            cost: { ...price, input: 4 },
          },
        ],
      },
    }),
  );
  const popup = await openSpend(page, request);
  const model = popup.locator(".token-cost-model-breakdown").filter({ hasText: "review-model" });
  await expect(model).toHaveCount(1);
  await expect(model.locator('.token-cost-model-row [data-scope="subagents"]')).toHaveText(
    "$19.00",
  );
  const input = model
    .getByRole("row")
    .filter({ has: page.getByRole("rowheader", { name: "Input", exact: true }) });
  await expect(input.locator('[data-scope="subagents"]')).toHaveText("4.0Mmixed rates$12.00");
  await expect(popup.getByTestId("subagent-cost-row")).toHaveCSS("border-top-style", "solid");
  await expect(popup.getByTestId("conversation-cost-subtotal")).toHaveCSS(
    "border-top-style",
    "solid",
  );
});

test("does not count failed pricing lookups as confirmed unpriced calls", async ({
  page,
  request,
}) => {
  await page.route("**/api/conversation/*/subagent-usage", (route) =>
    route.fulfill({ json: usage }),
  );
  const popup = await openSpend(page, request, { pricingFailed: true });
  await expect(popup).toContainText("Pricing lookup failed");
  await expect(popup).toContainText("1 call has no model pricing");
  await expect(popup).not.toContainText("3 calls have no model pricing");
  const indirect = popup
    .getByRole("row")
    .filter({ has: page.getByRole("rowheader", { name: "compaction", exact: true }) });
  const details = indirect.locator(".token-cost-cell");
  const calls = (await details.getByText("1 call", { exact: true }).boundingBox())!;
  const unpriced = (await details.getByText("no pricing", { exact: true }).boundingBox())!;
  expect(
    calls.x + calls.width <= unpriced.x ||
      unpriced.x + unpriced.width <= calls.x ||
      calls.y + calls.height <= unpriced.y ||
      unpriced.y + unpriced.height <= calls.y,
  ).toBe(true);
});

test("keeps a tall breakdown above its toggle on short desktop viewports", async ({
  page,
  request,
}) => {
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.route("**/api/conversation/*/subagent-usage", (route) =>
    route.fulfill({ json: usage }),
  );
  const popup = await openSpend(page, request);
  await expect(popup.getByTestId("token-cost-total")).toContainText("$11.98");
  const label = page.locator(".context-usage-label:visible");
  await expect
    .poll(async () => {
      const popupBox = (await popup.boundingBox())!;
      const labelBox = (await label.boundingBox())!;
      return popupBox.y + popupBox.height - labelBox.y;
    })
    .toBeLessThanOrEqual(0);
  await label.click();
  await expect(popup).toBeHidden();
});

for (const width of [768, 1280]) {
  test(`aligns shared model usage in one table (${width}px)`, async ({ page, request }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.route("**/api/conversation/*/subagent-usage", (route) =>
      route.fulfill({
        json: { ...usage, per_model: [{ ...reviewer, model: "main-model" }, unpriced] },
      }),
    );
    const popup = await openSpend(page, request);
    const table = popup.getByRole("table", { name: "Spend by model" });
    await expect(table).toBeVisible();
    await expect(table.getByRole("columnheader")).toHaveText([
      "Model / tokens",
      "Main conversation",
      "Sub-agents",
    ]);
    const model = table
      .locator(".token-cost-model-breakdown")
      .filter({ has: page.locator(".token-cost-model-name", { hasText: "main-model" }) });
    await expect(model).toHaveCount(1);
    const input = model
      .getByRole("row")
      .filter({ has: page.getByRole("rowheader", { name: "Input", exact: true }) });
    await expect(input.getByRole("cell").nth(0)).toContainText("1.0M");
    await expect(input.getByRole("cell").nth(0)).toContainText("$2.00");
    await expect(input.getByRole("cell").nth(1)).toContainText("2.0M");
    await expect(input.getByRole("cell").nth(1)).toContainText("$4.00");
    const mainBox = (await input.getByRole("cell").nth(0).boundingBox())!;
    const subBox = (await input.getByRole("cell").nth(1).boundingBox())!;
    expect(mainBox.y).toBe(subBox.y);
    expect(subBox.x).toBeGreaterThanOrEqual(mainBox.x + mainBox.width);
    expect(await table.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
    await expect(popup.getByRole("region", { name: "Sub-agents", exact: true })).toHaveCount(0);
  });
}
