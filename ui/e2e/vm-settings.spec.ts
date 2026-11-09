import { test, expect } from "@playwright/test";

// The prod tab's VM view: a Settings tab, first, and a Page tab, whose changes go
// through Shelley's typed API. exe.dev itself is mocked: no real VM changes.
test.beforeEach(async ({ request }) => {
  for (const name of ["workspace", "dev-prod-tabs"]) {
    const resp = await request.post("/feature-flags", { data: { name, value: true } });
    expect(resp.ok()).toBeTruthy();
  }
});
test.afterEach(async ({ request }) => {
  for (const name of ["workspace", "dev-prod-tabs"]) {
    const resp = await request.delete("/feature-flags", { data: { name } });
    expect(resp.ok()).toBeTruthy();
  }
});

test("a VM opens on its Settings tab, beside its Page tab, and settings changes confirm first", async ({
  page,
}) => {
  const vm = {
    name: "web",
    emoji: "🌐",
    status: "running",
    url: "https://web.exe.test",
    ssh_command: "ssh web.exe.test",
    public: false,
    comment: "",
    tags: ["scope"],
    role: "",
    integration: "acme-exe",
  };
  const settings = {
    vm_name: "web",
    emoji: "🌐",
    status: "running",
    region_display: "Los Angeles, USA",
    image: "exeuntu",
    pool_name: "",
    created_at: "2026-11-01T00:00:00Z",
    comment: "",
    tags: ["scope"],
    https_url: "https://web.exe.test",
    ssh_command: "ssh web.exe.test",
    shelley_url: "",
    terminal_url: "",
    allocated_cpus: 2,
    memory_capacity_bytes: 8 << 30,
    disk_capacity_bytes: 25 << 30,
    proxy_port: 8000,
    proxy_share: "private",
    sharing: { named_user_shares: null, team_shared: false },
    share_links: null,
    domains: null,
    egress_policy: "all",
    email_receive_enabled: false,
    email_address: "",
    integration: "acme-exe",
    scope_tag: "scope",
    role: "",
    denied: { "domain-add": "Its key reaches only VMs tagged scope." },
  };
  const changes: unknown[] = [];
  await page.route("**/api/exe-vms", (route) => route.fulfill({ json: { vms: [vm] } }));
  await page.route("**/api/exe-vms/web/settings", async (route) => {
    if (route.request().method() === "POST") {
      const change = route.request().postDataJSON();
      changes.push(change);
      settings.comment = change.value;
      return route.fulfill({ status: 204 });
    }
    return route.fulfill({ json: settings });
  });
  await page.route("**/api/exe-vms/web/frame?*", (route) =>
    route.fulfill({ json: { blocked: "" } }),
  );
  await page.route("https://web.exe.test/**", (route) => route.fulfill({ body: "the web page" }));

  await page.goto("/vm/web");
  await expect(page.getByTestId("view-tab-settings")).toHaveAttribute("aria-current", "page");
  const tabs = page.getByRole("navigation", { name: "VM tabs" }).getByRole("button");
  await expect(tabs.first()).toHaveAttribute("data-testid", "view-tab-settings");
  await expect(page.getByTestId("vm-status")).toContainText("running");
  await expect(page.getByTestId("vm-denied")).toContainText("tagged scope");
  await expect(page.getByTestId("vm-domain-add")).toBeDisabled();

  await page.getByTestId("vm-comment-edit").click();
  await page.getByTestId("vm-change-value").fill("staging");
  expect(changes).toEqual([]);
  await page.getByTestId("vm-change-confirm").click();
  await expect(page.getByTestId("vm-comment")).toHaveText("staging");
  expect(changes).toEqual([{ kind: "comment", value: "staging" }]);

  // Making it public, from the Share dialog, warns first.
  await page.getByTestId("vm-share").click();
  await page.getByTestId("vm-visibility-toggle").click();
  await expect(
    page.getByText("Anyone on the internet can reach https://web.exe.test"),
  ).toBeVisible();
  await page.keyboard.press("Escape");
  await page.keyboard.press("Escape");
  expect(changes).toHaveLength(1);

  // Delete wants the VM's name typed.
  await page.getByTestId("vm-delete").click();
  await page.getByTestId("vm-change-typed").fill("we");
  await expect(page.getByTestId("vm-change-confirm")).toBeDisabled();
  await page.getByTestId("vm-change-typed").fill("web");
  await expect(page.getByTestId("vm-change-confirm")).toBeEnabled();
  await page.keyboard.press("Escape");
  expect(changes).toHaveLength(1);

  await page.getByTestId("view-tab-page").click();
  await expect(page).toHaveURL(/\/vm\/web\/page$/);
  await expect(page.getByTestId("vm-frame")).toBeVisible();

  await page.getByTestId("view-tab-settings").click();
  await expect(page).toHaveURL(/\/vm\/web$/);
  await expect(page.getByTestId("vm-status")).toBeVisible();
});

test("a VM's Terminal tab attaches to its web terminal sessions, until the VM's view closes", async ({
  page,
}) => {
  const vm = {
    name: "web",
    emoji: "🌐",
    status: "running",
    url: "https://web.exe.test",
    ssh_command: "ssh web.exe.test",
    public: false,
    comment: "",
    tags: [],
    role: "",
    integration: "acme-exe",
  };
  await page.route("**/api/exe-vms", (route) => route.fulfill({ json: { vms: [vm] } }));
  await page.route("**/api/exe-vms/web/frame?*", (route) =>
    route.fulfill({ json: { blocked: "" } }),
  );
  await page.route("https://web.exe.test/**", (route) => route.fulfill({ body: "the web page" }));
  await page.route("**/api/exe-vms/web/settings", (route) => route.fulfill({ status: 502 }));
  let live = ["ios-1", "coral"];
  await page.route("**/api/exe-vms/web/terminal-sessions", (route) =>
    route.fulfill({ json: { sessions: live } }),
  );
  const closed: string[] = [];
  await page.route("**/api/exe-vms/web/terminal-sessions/*", (route) => {
    expect(route.request().method()).toBe("DELETE");
    const session = route.request().url().split("/").pop() ?? "";
    closed.push(session);
    live = live.filter((s) => s !== session);
    return route.fulfill({ status: 204 });
  });
  // The server's side of each session (TestExeVMTerminal covers the real one).
  const messages: { session: string; type: string; cols?: number; data?: string }[] = [];
  const open = new Set<string>();
  await page.routeWebSocket("**/api/exe-vms/web/terminal?*", (ws) => {
    const session = new URL(ws.url()).searchParams.get("session") ?? "";
    open.add(session);
    ws.onMessage((raw) => {
      const msg = JSON.parse(String(raw));
      messages.push({ session, ...msg });
      if (msg.type === "init") ws.send(JSON.stringify({ type: "output", data: btoa("$ ") }));
    });
    ws.onClose(() => open.delete(session));
  });

  await page.goto("/vm/web/page");
  const tabs = page.getByRole("navigation", { name: "VM tabs" }).getByRole("button");
  await expect(tabs).toHaveCount(3);
  await expect(tabs.last()).toHaveAttribute("data-testid", "view-tab-terminal");
  // Unopened, there is no shell.
  await expect(page.getByTestId("vm-terminal")).toHaveCount(0);

  await page.getByTestId("view-tab-terminal").click();
  await expect(page).toHaveURL(/\/vm\/web\/terminal$/);
  await expect(page.getByTestId("vm-terminal")).toBeVisible();
  // The VM's sessions, the newest attached.
  const sessionTabs = page.getByTestId("vm-terminal-session");
  await expect(sessionTabs).toHaveText(["ios-1", "coral"]);
  await expect(sessionTabs.last()).toHaveAttribute("aria-selected", "true");
  await expect.poll(() => messages[0]?.type).toBe("init");
  expect(messages[0].session).toBe("coral");
  expect(messages[0].cols).toBeGreaterThan(20);
  await page.keyboard.type("ls");
  await expect
    .poll(() => messages.map((m) => (m.session === "coral" ? (m.data ?? "") : "")).join(""))
    .toContain("ls");
  await page.screenshot({ path: "test-results/vm-terminal.png" });

  // Picking another attaches to it too, showing only it; switching back
  // reuses the first one's connection. A new one gets a color name, as the
  // web terminal's do.
  await sessionTabs.first().click();
  await expect.poll(() => [...open].sort()).toEqual(["coral", "ios-1"]);
  await expect(page.getByTestId("vm-terminal")).toHaveCount(2);
  await expect(page.getByTestId("vm-terminal").filter({ visible: true })).toHaveCount(1);
  await sessionTabs.nth(1).click();
  await page.keyboard.type("pwd");
  await expect
    .poll(() => messages.map((m) => (m.session === "coral" ? (m.data ?? "") : "")).join(""))
    .toContain("pwd");
  expect(messages.filter((m) => m.type === "init").map((m) => m.session)).toEqual([
    "coral",
    "ios-1",
  ]);
  await page.getByTestId("vm-terminal-new").click();
  await expect(sessionTabs).toHaveCount(3);
  const minted = (await sessionTabs.last().textContent())?.trim() ?? "";
  expect(minted).toMatch(/^[a-z]+$/);
  await expect.poll(() => [...open].sort()).toEqual(["coral", "ios-1", minted].sort());

  // Another tab keeps the sessions attached; leaving the VM detaches.
  await page.getByTestId("view-tab-page").click();
  await expect(page.getByTestId("vm-terminal").filter({ visible: true })).toHaveCount(0);
  expect([...open].sort()).toEqual(["coral", "ios-1", minted].sort());
  await page.goto("/");
  await expect.poll(() => [...open]).toEqual([]);

  // Coming back attaches to the session last attached, while it lasts;
  // closing it ends it and attaches to the newest left.
  live = ["ios-1", "coral", minted];
  await page.goto("/vm/web/terminal");
  await expect(sessionTabs.last()).toHaveAttribute("aria-selected", "true");
  await expect.poll(() => [...open]).toEqual([minted]);
  await page.getByTestId("vm-terminal-close").last().click();
  await expect(sessionTabs).toHaveText(["ios-1", "coral"]);
  expect(closed).toEqual([minted]);
  await expect.poll(() => [...open]).toEqual(["coral"]);
});
