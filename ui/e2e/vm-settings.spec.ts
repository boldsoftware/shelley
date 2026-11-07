import { test, expect } from "@playwright/test";

// The prod tab's VM view: a Page tab and a Settings tab, whose changes go
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

test("a VM has Page and Settings tabs, and settings changes confirm first", async ({ page }) => {
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
  await expect(page.getByTestId("view-tab-page")).toHaveAttribute("aria-current", "page");
  await expect(page.getByTestId("vm-frame")).toBeVisible();

  await page.getByTestId("view-tab-settings").click();
  await expect(page).toHaveURL(/\/vm\/web\/settings$/);
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
  await expect(page).toHaveURL(/\/vm\/web$/);
  await expect(page.getByTestId("vm-frame")).toBeVisible();
});
