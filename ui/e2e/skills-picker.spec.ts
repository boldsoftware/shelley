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

test("mobile skill sheet opens without focusing its search field", async ({ page, request }) => {
  await withTempDir("shelley-skills-", async (cwd) => {
    await openConversation(page, request, cwd);
    await page.getByTestId("message-input").fill("/skills draft-review");
    const picker = page.getByTestId("skill-picker");
    await expect(picker.getByRole("option")).toHaveCount(1);
    await expect(picker.getByRole("searchbox", { name: "Find a skill" })).not.toBeFocused();
    await picker.getByRole("button", { name: "Close skills" }).click();
    await expect(picker).toBeHidden();
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
