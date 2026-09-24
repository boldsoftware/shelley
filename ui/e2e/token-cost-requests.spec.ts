import { expect, test } from "@playwright/test";
import type { Message } from "../src/types";

for (const priced of [true, false]) {
  test(`counts paused requests alongside legacy usage (priced=${priced})`, async ({
    page,
    request,
  }) => {
    await page.route("**/api/model-costs", (route) =>
      route.fulfill({
        json: {
          costs: {
            model: priced ? { input: 2, output: 8, cache_read: 0.2, cache_write: 2.5 } : null,
            unknown: null,
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
    const messages = body.messages as Message[];
    const human = messages.find((m) => m.type === "user")!;
    const agents = messages.filter((m) => m.type === "agent");
    expect(agents).toHaveLength(2);
    const start = "2026-06-01T12:00:00Z";
    human.created_at = start;
    human.other_usage_data = null;
    const first = {
      input_tokens: 1_000_000,
      cache_creation_input_tokens: 0,
      cache_read_input_tokens: 0,
      output_tokens: 0,
      cost_usd: 0.25,
      start_time: start,
      end_time: "2026-06-01T12:00:01Z",
    };
    const second = {
      ...first,
      input_tokens: 2_000_000,
      cost_usd: 0.5,
      start_time: "2026-06-01T12:00:02Z",
      end_time: "2026-06-01T12:00:04Z",
    };
    const paused = {
      ...first,
      model: "model",
      input_tokens: 3_000_000,
      cost_usd: 0.75,
      end_time: second.end_time,
      requests: [first, second],
    };
    agents[0].created_at = "2026-06-01T12:00:09Z"; // Later storage must not replace request timing.
    agents[0].end_of_turn = false;
    agents[0].usage_data = JSON.stringify(paused);
    agents[0].other_usage_data = JSON.stringify([{ ...paused, purpose: "llm_one_shot" }]);
    agents[1].created_at = "2026-06-01T12:00:06Z";
    agents[1].end_of_turn = true;
    agents[1].usage_data = JSON.stringify({
      ...first,
      model: "model",
      cost_usd: 1,
      start_time: undefined,
      end_time: undefined,
    });
    agents[1].other_usage_data = JSON.stringify([
      { purpose: "llm_one_shot", model: "unknown", input_tokens: 100, cost_usd: 0.5 },
    ]);
    body.messages = [human, ...agents];
    await page.route(`**/api/conversation/${id}`, (route) => route.fulfill({ json: body }));
    await page.goto(`/c/${body.conversation.slug}`);
    await page.locator(".context-usage-label:visible").click();
    const popup = page.locator(".chat-context-popup");
    await expect(popup.locator(".token-cost-graph-svg")).toContainText("LLM call number (3 calls)");
    await expect(popup.getByTestId("token-cost-total")).toHaveText(
      priced ? "Total≈$14.50" : "Total≈$3.00",
    );
    await expect(popup).toContainText(
      priced ? "Provider-reported direct cost: $1.75" : "Provider-reported $1.75",
    );
    const indirect = popup
      .getByRole("row")
      .filter({ has: page.getByRole("rowheader", { name: "llm_one_shot", exact: true }) });
    await expect(indirect).toContainText("3 calls");
    await expect(indirect).toContainText(priced ? "$6.50" : "$1.25");
    await expect(popup).toContainText(
      priced ? "1 call has no model pricing" : "6 calls have no model pricing",
    );
    await popup.getByRole("button", { name: "time", exact: true }).click();
    await expect(popup.locator(".token-cost-graph-svg")).toContainText("6s active");
  });
}
