import { expect, test, type Locator } from "@playwright/test";
import type { Message } from "../src/types";
import { createConversationViaAPIWithDetails } from "./helpers";

test("shows only the latest system prompt after in-place compaction refreshes it", async ({
  page,
  request,
}) => {
  const { conversationId, slug } = await createConversationViaAPIWithDetails(
    request,
    "echo: refreshed context card",
  );
  const response = await request.get(`/api/conversation/${conversationId}`);
  expect(response.ok()).toBeTruthy();
  const body = await response.json();
  const original = body.messages.find((message: Message) => message.type === "system");
  expect(original).toBeTruthy();
  const lastSequence = body.messages.at(-1).sequence_id;
  for (let revision = 1; revision <= 2; revision++) {
    body.messages.push({
      ...original,
      message_id: `refreshed-system-${revision}`,
      sequence_id: lastSequence + revision,
      llm_data: JSON.stringify({
        Content: [{ Type: 2, Text: `Refreshed system prompt ${revision}` }],
      }),
      display_data: JSON.stringify({ tools: [], skills: [] }),
    });
  }
  await page.route("**/api/stream2*", (route) => route.abort());
  await page.route(`**/api/conversation/${conversationId}`, (route) =>
    route.fulfill({ json: body }),
  );
  await page.goto(`/c/${slug}`);

  const card = page.getByTestId("generation-context");
  await expect(card.locator(":scope > .system-prompt-view")).toHaveCount(1);
  await card.getByRole("button", { name: /System Prompt:/ }).click();
  await expect(card.locator(".system-prompt-text")).toHaveText("Refreshed system prompt 2");
  await expect(card.locator(".model-bar")).toBeVisible();
});

test("groups model and system prompt into one context card", async ({ page, request }) => {
  await page.setViewportSize({ width: 1000, height: 800 });
  const conversation = await createConversationViaAPIWithDetails(request, "echo: context card");

  await page.goto(`/c/${conversation.slug}`);
  await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

  const card = page.getByTestId("generation-context");
  await expect(card).toBeVisible();
  await expect(card.locator(":scope > .model-bar")).toBeVisible();
  await expect(card.locator(":scope > .system-prompt-view")).toBeVisible();
  const modelSummary = card.locator(":scope > .model-bar");
  const promptButton = card.getByRole("button", {
    name: /System Prompt:\s*\d+ tools?\s*,?\s*\d+ skills? Expand/,
  });
  await expect(promptButton).toBeVisible();
  await expect(modelSummary).toContainText("Model:");
  await expect(modelSummary).not.toContainText("Reasoning");
  await expect(promptButton).toContainText(/System Prompt:\s*\d+ tools?,\s*\d+ skills?/);
  expect(
    await promptButton.evaluate((element) => ({
      boxShadow: getComputedStyle(element).boxShadow,
      borderLeftWidth: getComputedStyle(element).borderLeftWidth,
    })),
  ).toEqual({ boxShadow: "none", borderLeftWidth: "0px" });

  const modelName = modelSummary.locator(".model-bar-name");
  await modelName.evaluate((element) => {
    element.textContent = "a-very-long-custom-model-name-".repeat(12);
  });
  const [cardRect, promptRect] = await Promise.all([
    card.evaluate((element) => element.getBoundingClientRect().toJSON()),
    promptButton.evaluate((element) => element.getBoundingClientRect().toJSON()),
  ]);
  expect(promptRect.width).toBeGreaterThan(200);
  expect(promptRect.right).toBeLessThanOrEqual(cardRect.right + 1);
  // The long name ellipsizes; ", <reasoning>" stays fully visible (not
  // clipped by any overflow ancestor) after it.
  expect(await modelName.evaluate((e) => e.scrollWidth > e.clientWidth)).toBe(true);
  expect(
    await modelSummary.locator(".model-bar-reasoning").evaluate((element) => {
      const range = document.createRange();
      range.selectNodeContents(element);
      const text = range.getBoundingClientRect();
      const y = text.top + text.height / 2;
      return [text.left + 1, text.right - 1].map(
        (x) => document.elementFromPoint(x, y) === element,
      );
    }),
  ).toEqual([true, true]);

  const lineRects = await Promise.all([
    modelSummary.evaluate((element) => {
      const rect = element.getBoundingClientRect();
      return { top: rect.top, bottom: rect.bottom };
    }),
    promptButton.evaluate((element) => {
      const rect = element.getBoundingClientRect();
      return { top: rect.top, bottom: rect.bottom };
    }),
  ]);
  expect(Math.abs(lineRects[0].top - lineRects[1].top)).toBeLessThan(1);
  expect(Math.abs(lineRects[0].bottom - lineRects[1].bottom)).toBeLessThan(1);

  await page.setViewportSize({ width: 540, height: 800 });
  await expect(promptButton).toBeVisible();

  const skillCount = Number((await promptButton.textContent())?.match(/(\d+) skills?/)?.[1]);
  const toolCount = Number((await promptButton.textContent())?.match(/(\d+) tools?/)?.[1]);
  expect(skillCount).toBeGreaterThan(0);
  expect(toolCount).toBeGreaterThan(0);
  await promptButton.click();
  await expect(
    card.getByRole("button", {
      name: /System Prompt:\s*\d+ tools?\s*,?\s*\d+ skills? Collapse/,
    }),
  ).toBeVisible();
  await expect(card.locator(".system-prompt-content")).toBeVisible();
  await expect(card.locator(".system-prompt-skill-item")).toHaveCount(skillCount);
  await expect(card.locator(".system-prompt-tool-item")).toHaveCount(toolCount);

  // Integration skills may precede built-ins (and have URL sources). Select a
  // known built-in rather than depending on the runner's integrations/order.
  const skillCard = card.locator(".system-prompt-skill-item").filter({
    has: page.locator(".system-prompt-card-name", { hasText: /^commit-highlights$/ }),
  });
  await skillCard.locator(".system-prompt-card-summary").click();
  await expect(skillCard.locator(".system-prompt-card-detail")).toBeVisible();
  await expect(skillCard.locator(".system-prompt-card-detail")).toContainText("Source");
  await expect(skillCard.locator(".system-prompt-card-detail code").first()).toContainText(
    "SKILL.md",
  );

  const toolCard = card.locator(".system-prompt-tool-item").first();
  await toolCard.locator(".system-prompt-card-summary").click();
  await expect(toolCard.locator(".system-prompt-card-detail")).toBeVisible();
  await expect(toolCard.locator(".system-prompt-card-detail")).toContainText("Source");
  await expect(toolCard.locator(".system-prompt-card-detail code").first()).toContainText(
    "claudetool/",
  );

  const childSurfaces = await card
    .locator(":scope > .model-bar, :scope > .system-prompt-view")
    .evaluateAll((elements) =>
      elements.map((element) => {
        const style = getComputedStyle(element);
        return {
          backgroundColor: style.backgroundColor,
          borderTopWidth: style.borderTopWidth,
          borderRightWidth: style.borderRightWidth,
          borderBottomWidth: style.borderBottomWidth,
          borderLeftWidth: style.borderLeftWidth,
        };
      }),
    );
  expect(childSurfaces).toEqual([
    {
      backgroundColor: "rgba(0, 0, 0, 0)",
      borderTopWidth: "0px",
      borderRightWidth: "0px",
      borderBottomWidth: "0px",
      borderLeftWidth: "0px",
    },
    {
      backgroundColor: "rgba(0, 0, 0, 0)",
      borderTopWidth: "0px",
      borderRightWidth: "0px",
      borderBottomWidth: "0px",
      borderLeftWidth: "0px",
    },
  ]);
});

test("model and system prompt items share one look", async ({ page, request }) => {
  await page.setViewportSize({ width: 1400, height: 800 });
  const conversation = await createConversationViaAPIWithDetails(request, "echo: context styles");
  await page.goto(`/c/${conversation.slug}`);
  const card = page.getByTestId("generation-context");
  const rows = [
    card.locator(":scope > .model-bar"),
    card.getByRole("button", { name: /System Prompt:/ }),
  ];
  await expect(rows[0]).toBeVisible();
  await expect(rows[1]).toBeVisible();

  // Each row is [icon tile] [label] [value]; the value follows the label.
  const parts = (row: Locator) => {
    const label = row.getByText(/^(Model|System Prompt):$/);
    return {
      icon: row.locator("[aria-hidden='true']").first(),
      label,
      value: label.locator("xpath=following-sibling::*[1]"),
    };
  };
  const look = (el: Locator, props: string[]) =>
    el.evaluate(
      (e, names) =>
        Object.fromEntries(names.map((n) => [n, getComputedStyle(e).getPropertyValue(n)])),
      props,
    );
  const text = ["font-family", "font-size", "font-weight", "color"];
  const tile = ["width", "height", "background-color", "border-radius", "font-size"];
  const box = (el: Locator) => el.evaluate((e) => e.getBoundingClientRect().toJSON() as DOMRect);
  const spacing = async (row: Locator) => {
    const { icon, label, value } = parts(row);
    const [i, l, v] = await Promise.all([box(icon), box(label), box(value)]);
    return [Math.round(l.left - i.right), Math.round(v.left - l.right)];
  };

  const [model, prompt] = rows.map(parts);
  expect(await look(model.label, text)).toEqual(await look(prompt.label, text));
  expect(await look(model.value, text)).toEqual(await look(prompt.value, text));
  expect(await look(model.icon, tile)).toEqual(await look(prompt.icon, tile));
  expect(await spacing(rows[0])).toEqual(await spacing(rows[1]));

  // "Claude Opus 5.5, xhigh" and "9 tools, 11 skills": every comma sits right
  // against the glyph before it, with no space or flex gap in between.
  for (const row of rows) {
    const gaps = await row.evaluate((root) => {
      const glyphs: { ch: string; rect: DOMRect }[] = [];
      const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
      for (let node = walker.nextNode(); node; node = walker.nextNode()) {
        const s = node.textContent ?? "";
        for (let i = 0; i < s.length; i++) {
          const range = document.createRange();
          range.setStart(node, i);
          range.setEnd(node, i + 1);
          const rect = range.getBoundingClientRect();
          if (rect.width > 0 && s[i].trim()) glyphs.push({ ch: s[i], rect });
        }
      }
      return glyphs.flatMap((g, i) =>
        g.ch === "," && i > 0 ? [g.rect.left - glyphs[i - 1].rect.right] : [],
      );
    });
    expect(gaps).toHaveLength(1);
    expect(Math.abs(gaps[0])).toBeLessThan(1);
  }
  await expect(rows[0]).toContainText(/Model:\s*\S.*\S, \S+$/);

  // Phones hide the labels but keep the icon tiles and values identical. The
  // layout switches on a media query listener, so it settles after the resize.
  await page.setViewportSize({ width: 390, height: 800 });
  await expect(rows[1]).toBeVisible();
  await expect(async () => {
    for (const { label } of [model, prompt]) {
      expect((await box(label)).width).toBeLessThanOrEqual(1);
    }
    expect(await look(model.value, text)).toEqual(await look(prompt.value, text));
    expect(await look(model.icon, tile)).toEqual(await look(prompt.icon, tile));
  }).toPass();
});
