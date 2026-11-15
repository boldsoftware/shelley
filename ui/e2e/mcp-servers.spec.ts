import { expect, test, type Page } from "@playwright/test";
import type { McpServer } from "../src/services/api";

const servers: McpServer[] = [
  {
    name: "kagi",
    url: "https://kagi-mcp.int.exe.xyz/mcp",
    description: "Integration-managed authentication",
    headers: {},
    auth: "",
    integration: "kagi-mcp",
  },
  {
    name: "keyed",
    url: "https://keyed.example.test/mcp",
    description: "Static API key",
    headers: { Authorization: "Bearer key" },
    auth: "",
  },
  {
    name: "oauth",
    url: "https://oauth.example.test/mcp",
    description: "Direct OAuth",
    headers: {},
    auth: "login_required",
    login_url: "/mcp/login/oauth",
  },
  {
    name: "logged-in",
    url: "https://logged-in.example.test/mcp",
    description: "Direct OAuth with a stored login",
    headers: {},
    auth: "logged_in",
    login_url: "/mcp/login/logged-in",
  },
];

async function openPanel(page: Page) {
  await page.goto("/new");
  await page.keyboard.press("Control+k");
  await page.locator(".command-palette-input").fill("MCP Servers");
  await page.locator(".command-palette-item").first().click();
  const panel = page.getByRole("dialog", { name: "MCP Servers", exact: true });
  await expect(panel).toBeVisible();
  return panel;
}

test("integration-managed servers have no login, edit, or delete controls", async ({ page }) => {
  await page.route("**/api/mcp/servers", (route) => route.fulfill({ json: servers }));
  const panel = await openPanel(page);
  for (const name of ["kagi", "keyed"]) {
    const row = panel.getByRole("row").filter({
      has: page.getByText(name, { exact: true }),
    });
    await expect(row).toBeVisible();
    await expect(row.getByRole("link", { name: "Sign in", exact: true })).toHaveCount(0);
  }
  await expect(panel.getByRole("link", { name: "Sign in", exact: true })).toHaveAttribute(
    "href",
    "/mcp/login/oauth",
  );
  await expect(panel.getByRole("button", { name: "Log out", exact: true })).toBeVisible();
  await expect(panel.getByRole("button", { name: "Delete kagi", exact: true })).toHaveCount(0);
  await expect(panel.getByRole("button", { name: "Edit kagi", exact: true })).toHaveCount(0);
  await expect(panel.getByRole("button", { name: "Delete keyed", exact: true })).toBeVisible();
  await expect(panel.getByRole("button", { name: "Delete oauth", exact: true })).toBeVisible();
});

test("refresh clears errors and reloads servers with a loading state", async ({ page }) => {
  let response: "error" | "pending" | "success" = "error";
  let release!: () => void;
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/api/mcp/servers", async (route) => {
    if (response === "error") {
      await route.fulfill({ status: 502, body: "Integration discovery failed" });
      return;
    }
    if (response === "pending") await pending;
    await route.fulfill({ json: servers.slice(0, 2) });
  });
  const panel = await openPanel(page);
  const refresh = panel.getByRole("button", { name: "Refresh", exact: true });
  await expect(panel.getByRole("alert")).toContainText("Integration discovery failed");
  response = "pending";
  await refresh.click();
  await expect(refresh).toBeDisabled();
  await expect(panel.getByRole("alert")).toHaveCount(0);
  await expect(panel.locator(".models-loading")).toBeVisible();
  response = "success";
  release();
  await expect(panel.getByText("kagi", { exact: true })).toBeVisible();
  await expect(refresh).toBeEnabled();
  await expect(panel.getByRole("columnheader", { name: "Login", exact: true })).toHaveCount(0);
});

test("refresh removes pending deletion when an integration takes ownership", async ({ page }) => {
  let managed = false;
  await page.route("**/api/mcp/servers", (route) =>
    route.fulfill({
      json: [{ ...servers[0], integration: managed ? "kagi-mcp" : undefined }],
    }),
  );
  const panel = await openPanel(page);
  await panel.getByRole("button", { name: "Delete kagi", exact: true }).click();
  await expect(panel.getByRole("button", { name: "Delete", exact: true })).toBeVisible();
  managed = true;
  await panel.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(panel.getByText("kagi", { exact: true })).toBeVisible();
  await expect(panel.getByRole("button", { name: /^Delete/ })).toHaveCount(0);
});

test("public servers need no manual connection or login", async ({ page }) => {
  let probes = 0;
  await page.route("**/api/mcp/servers", (route) =>
    route.fulfill({ json: [{ ...servers[2], auth: "" }] }),
  );
  await page.route("**/api/mcp/servers/oauth/tools", (route) => {
    probes++;
    return route.fulfill({ json: { tools: [] } });
  });
  const panel = await openPanel(page);
  await expect(panel.getByRole("link", { name: "Sign in", exact: true })).toHaveCount(0);
  await expect(panel.getByRole("columnheader", { name: "Login", exact: true })).toHaveCount(0);
  await expect(panel.getByRole("button", { name: "Connect", exact: true })).toHaveCount(0);
  await expect(panel.getByRole("button", { name: "Tools", exact: true })).toBeVisible();
  expect(probes).toBe(0);
  await panel.getByRole("button", { name: "Tools", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "oauth tools", exact: true })).toContainText(
    "This server has no tools.",
  );
  expect(probes).toBe(1);
});

test("sign-in appears only after an authentication challenge", async ({ page }) => {
  let auth = "";
  await page.route("**/api/mcp/servers", (route) =>
    route.fulfill({ json: [{ ...servers[2], auth }] }),
  );
  const panel = await openPanel(page);
  await expect(panel.getByRole("link", { name: "Sign in", exact: true })).toHaveCount(0);
  auth = "login_required";
  await panel.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(panel.getByRole("link", { name: "Sign in", exact: true })).toHaveAttribute(
    "href",
    "/mcp/login/oauth",
  );
});

for (const { index, label, href } of [
  { index: 0, label: "Manage on exe.dev", href: "https://exe.dev/integrations" },
  { index: 2, label: "Sign in", href: "/mcp/login/oauth" },
]) {
  test(`${label} is actionable and tools retry when returning from authentication`, async ({
    page,
  }) => {
    const server: McpServer = { ...servers[index], auth: "" };
    let authenticated = false;
    let probes = 0;
    await page.route("**/api/mcp/servers", (route) => route.fulfill({ json: [server] }));
    await page.route(`**/api/mcp/servers/${server.name}/tools`, (route) => {
      probes++;
      return authenticated
        ? route.fulfill({
            json: { tools: [{ name: "ready_tool", inputSchema: { properties: {} } }] },
          })
        : route.fulfill({
            status: 401,
            body: `MCP server needs authentication: /mcp/login/${server.name}`,
          });
    });
    const panel = await openPanel(page);
    await panel.getByRole("button", { name: "Tools", exact: true }).click();
    const tools = page.getByRole("dialog", { name: `${server.name} tools`, exact: true });
    const action = tools.getByRole("link", { name: label, exact: true });
    await expect(action).toHaveAttribute("href", href);
    await expect(action).toHaveAttribute("target", "_blank");
    await expect(tools.getByRole("alert")).toHaveCount(0);
    await expect(tools).not.toContainText("/mcp/login/");
    expect(probes).toBe(1);

    authenticated = true;
    await page.evaluate(() => window.dispatchEvent(new Event("focus")));
    await expect(tools.getByText("ready_tool", { exact: true })).toBeVisible();
    await expect(action).toHaveCount(0);
    expect(probes).toBe(2);
  });
}

for (const { status, index } of [
  { status: 502, index: 2 },
  { status: 401, index: 1 },
]) {
  test(`tools preserve HTTP ${status} errors without a known authentication action`, async ({
    page,
  }) => {
    const server = servers[index];
    await page.route("**/api/mcp/servers", (route) => route.fulfill({ json: [server] }));
    await page.route(`**/api/mcp/servers/${server.name}/tools`, (route) =>
      route.fulfill({ status, body: "Remote server failed" }),
    );
    const panel = await openPanel(page);
    await panel.getByRole("button", { name: "Tools", exact: true }).click();
    const tools = page.getByRole("dialog", { name: `${server.name} tools`, exact: true });
    await expect(tools.getByRole("alert")).toContainText("Remote server failed");
    await expect(tools.getByRole("link")).toHaveCount(0);
  });
}
