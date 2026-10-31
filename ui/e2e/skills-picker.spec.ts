import { expect, test, type Page } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { createConversationViaAPIWithDetails, withTempDir } from "./helpers";

const prefix = "Use the following skills: `draft-review`.\n\n";

// Opens a started conversation whose prompt lists one skill, ready to pick it.
async function openConversation(
  page: Page,
  request: Parameters<typeof createConversationViaAPIWithDetails>[0],
  cwd: string,
) {
  const dir = join(cwd, ".skills", "draft-review");
  mkdirSync(dir, { recursive: true });
  writeFileSync(
    join(dir, "SKILL.md"),
    "---\nname: draft-review\ndescription: Review a draft for clarity\n---\nRead the draft.\n",
  );
  const created = await createConversationViaAPIWithDetails(request, "echo: fixture", { cwd });
  await page.goto(`/c/${created.slug}`);
  return created.conversationId;
}

async function pickSkill(page: Page) {
  const input = page.getByTestId("message-input");
  await input.fill("/skills draft");
  await page.getByTestId("skill-picker").getByRole("option").first().click();
  await expect(page.getByTestId("skill-chip")).toContainText("draft-review");
  return input;
}

async function mockMcpTools(page: Page) {
  await page.route("**/api/mcp/servers", (route) =>
    route.fulfill({ json: [{ name: "web" }, { name: "library" }, { name: "locked" }] }),
  );
  await page.route("**/api/mcp/servers/*/tools", (route) => {
    const server = new URL(route.request().url()).pathname.split("/").at(-2);
    if (server === "locked")
      return route.fulfill({ status: 401, body: "Log in to locked" });
    return route.fulfill({
      json: {
        tools: [{
          name: "search",
          description: `Search ${server} sources`,
          inputSchema: {
            properties: { query: { type: "string", description: "Search terms" } },
            required: ["query"],
          },
        }],
      },
    });
  });
}

test.describe("desktop", () => {
  test.use({ viewport: { width: 1280, height: 800 } });

  test("hovering long MCP details keeps the picker height and position fixed", async ({ page, request }) => {
    await withTempDir("shelley-skills-", async (cwd) => {
      await page.route("**/api/mcp/servers", (route) =>
        route.fulfill({ json: [{ name: "web" }] }),
      );
      await page.route("**/api/mcp/servers/web/tools", (route) =>
        route.fulfill({
          json: { tools: [{
            name: "search",
            description: "Long MCP tool description. ".repeat(200),
            inputSchema: { properties: {} },
          }] },
        }),
      );
      await openConversation(page, request, cwd);
      await page.getByTestId("message-input").fill("/skills ");
      const picker = page.getByTestId("skill-picker");
      const skill = picker.getByRole("option").filter({ hasText: "draft-review" });
      const tool = picker.getByRole("option").filter({ hasText: "search" });
      await expect(tool).toHaveCount(1);
      await skill.hover();
      const bounds = await picker.boundingBox();
      expect(bounds?.height).toBe(420);
      await tool.hover();
      await expect(picker.locator(".skill-picker-detail")).toContainText("Long MCP tool description.");
      expect(await picker.boundingBox()).toEqual(bounds);
      expect(await picker.locator(".skill-picker-detail").evaluate(
        (el) => el.scrollHeight > el.clientHeight,
      )).toBe(true);
      await skill.hover();
      expect(await picker.boundingBox()).toEqual(bounds);
    });
  });

  test("choosing a skill adds a chip, clears /skills and sends nothing", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-skills-", async (cwd) => {
      await openConversation(page, request, cwd);
      const sent: string[] = [];
      page.on("request", (req) => {
        if (req.method() === "POST") sent.push(req.url());
      });
      const input = await pickSkill(page);
      await expect(input).toHaveValue("");
      await expect(page.getByTestId("skill-picker")).toBeHidden();
      expect(sent).toEqual([]);
    });
  });

  test("the chip can be removed", async ({ page, request }) => {
    await withTempDir("shelley-skills-", async (cwd) => {
      await openConversation(page, request, cwd);
      await pickSkill(page);
      await page.getByRole("button", { name: "Remove skill draft-review" }).click();
      await expect(page.getByTestId("skill-chip")).toHaveCount(0);
    });
  });

  test("sending carries the skill and the stored message has the prefix", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-skills-", async (cwd) => {
      const id = await openConversation(page, request, cwd);
      const input = await pickSkill(page);
      await input.fill("echo: please review");
      const posted = page.waitForRequest(
        (req) =>
          req.method() === "POST" && new URL(req.url()).pathname === `/api/conversation/${id}/chat`,
      );
      await page.getByTestId("send-button").click();
      expect((await posted).postDataJSON()).toMatchObject({
        message: "echo: please review",
        skills: ["draft-review"],
      });
      await expect(page.getByTestId("skill-chip")).toHaveCount(0);
      await expect(page.getByText("Use the following skills:")).toBeVisible();
      const state = await (await request.get(`/api/conversation/${id}`)).json();
      const stored = state.messages
        .filter((m: { type: string }) => m.type === "user")
        .map((m: { llm_data: string }) => JSON.parse(m.llm_data).Content[0].Text);
      expect(stored.at(-1)).toBe(prefix + "echo: please review");
    });
  });

  test("MCP tools show short names, server badges, schemas and partial errors, and send the exact tool", async ({ page, request }) => {
    await withTempDir("shelley-mcp-skills-", async (cwd) => {
      await mockMcpTools(page);
      const id = await openConversation(page, request, cwd);
      const input = page.getByTestId("message-input");
      await input.fill("/skills search");
      const picker = page.getByTestId("skill-picker");
      const options = picker.getByRole("option");
      await expect(options).toHaveCount(2);
      await expect(options.first().locator(".truncate")).toHaveText("search");
      await expect(options.first().locator(".skill-mcp-badge")).toHaveText("web");
      await expect(options.last().locator(".skill-mcp-badge")).toHaveText("library");
      await expect(picker.locator(".skill-picker-warning")).toContainText("locked");
      await expect(picker.locator(".skill-mcp-parameters")).toContainText("query");
      await expect(picker.locator(".skill-mcp-parameters")).toContainText("required");
      await expect(picker.locator(".skill-mcp-activate")).toContainText("'web.search'");
      await options.first().click();
      const chip = page.getByTestId("skill-chip");
      await expect(chip.locator(".skill-chip-label")).toHaveText("search");
      await expect(chip.locator(".skill-mcp-badge")).toHaveText("web");
      await expect(chip.getByRole("button", { name: "Remove tool search from web", exact: true })).toBeVisible();
      await input.fill("echo: find sources");
      const posted = page.waitForRequest((req) =>
        req.method() === "POST" && new URL(req.url()).pathname === `/api/conversation/${id}/chat`,
      );
      await page.getByTestId("send-button").click();
      const payload = (await posted).postDataJSON();
      expect(payload.skills).toEqual([]);
      expect(payload.message).toMatch(/^echo: find sources\n\nUse the MCP tool "web.search"/);
      await expect(chip).toHaveCount(0);
      await expect(page.locator(".message-user").last()).toContainText('Use the MCP tool "web.search"');
    });
  });
});

test("mobile skills fill the space above the composer and keep typing focus", async ({ page, request }) => {
  await withTempDir("shelley-skills-", async (cwd) => {
    await mockMcpTools(page);
    await openConversation(page, request, cwd);
    const input = page.getByTestId("message-input");
    await input.fill("/skills ");
    const picker = page.getByTestId("skill-picker");
    await expect(input).toBeFocused();
    await expect(picker.getByRole("searchbox")).toHaveCount(0);
    await expect.poll(async () => (await picker.boundingBox())?.y).toBeCloseTo(12, 0);
    expect((await picker.boundingBox())!.height).toBeGreaterThan(420);
    await input.pressSequentially("a-long-skill-query-that-wraps-across-multiple-lines");
    await expect(picker).toContainText("No matching skills");
    await expect.poll(() => picker.evaluate((el) =>
      Math.round(el.getBoundingClientRect().top - (window.visualViewport?.offsetTop ?? 0)),
    )).toBe(12);
    await picker.getByText("No matching skills").tap();
    await expect(input).toBeFocused();
    await expect(picker).toBeVisible();
    await input.fill("/skills ");
    await input.pressSequentially("draft-review");
    await expect(picker.getByRole("option")).toHaveCount(1);
    await expect(picker.getByRole("option")).toContainText("draft-review");
    await expect(input).toBeFocused();
    await page.setViewportSize({ width: 393, height: 500 });
    await expect.poll(() => picker.evaluate((el) => {
      const bounds = el.getBoundingClientRect();
      const input = document.querySelector('[data-testid="message-input"]')!.getBoundingClientRect();
      return {
        top: Math.round(bounds.top - (window.visualViewport?.offsetTop ?? 0)),
        gap: Math.round(input.top - bounds.bottom),
      };
    })).toEqual({ top: 12, gap: 6 });
    const bounds = (await picker.boundingBox())!;
    expect(bounds.height).toBeGreaterThan(200);
    await picker.getByRole("button", { name: "Close skills" }).click();
    await expect(picker).toBeHidden();
    await expect(input).toBeFocused();
  });
});

test("mobile MCP filtering and selection keep focus in the composer", async ({ page, request }) => {
  await withTempDir("shelley-skills-", async (cwd) => {
    await mockMcpTools(page);
    await openConversation(page, request, cwd);
    const input = page.getByTestId("message-input");
    await input.fill("/skills ");
    await expect(input).toBeFocused();
    await input.pressSequentially("library search");
    const picker = page.getByTestId("skill-picker");
    await expect(picker.getByRole("option")).toHaveCount(1);
    await expect(picker.locator(".skill-mcp-badge")).toHaveText("library");
    await expect(input).toBeFocused();
    await picker.getByRole("option").click();
    await expect(page.getByTestId("skill-chip")).toContainText("search");
    await expect(input).toHaveValue("");
    await expect(input).toBeFocused();
  });
});

test("mobile MCP tools remain distinct by badge and can both be selected", async ({ page, request }) => {
  await withTempDir("shelley-mcp-skills-", async (cwd) => {
    await mockMcpTools(page);
    await openConversation(page, request, cwd);
    const input = page.getByTestId("message-input");
    for (const server of ["web", "library"]) {
      await input.fill(`/skills ${server} search`);
      const picker = page.getByTestId("skill-picker");
      await expect(picker.getByRole("option")).toHaveCount(1);
      await expect(picker.locator(".skill-mcp-badge")).toHaveText(server);
      await expect(picker.locator(".skill-picker-description")).toHaveText(`Search ${server} sources`);
      await picker.getByRole("option").click();
    }
    await expect(page.getByTestId("skill-chip")).toHaveCount(2);
    await expect(page.getByTestId("skill-chip").locator(".skill-mcp-badge")).toHaveText(["web", "library"]);
  });
});
