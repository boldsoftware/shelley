import { test, expect, type Page } from "@playwright/test";
import { randomUUID } from "node:crypto";
import type { ExeAccountView, GithubAccount } from "../src/services/workspace";

// The exe.dev account as an exe-dev integration with full control sees it
// (this server's exe.dev is mocked at its API): GitHub connected or not, and
// the integrations the project editor adds.
const headers = { "X-Shelley-Request": "1" };
const setupURL = "https://exe.dev/github/setup";

function manyRepos(): GithubAccount[] {
  const repos = (owner: string, n: number) =>
    Array.from({ length: n }, (_, i) => ({
      full_name: `${owner}/repo-${String(i).padStart(3, "0")}`,
      private: i % 3 === 0,
    }));
  return [
    {
      account: "acme",
      github_login: "ann",
      error: "",
      repos: [{ full_name: "acme/web", private: true }, ...repos("acme", 90)],
    },
    { account: "ann", github_login: "ann", error: "", repos: repos("ann", 30) },
  ];
}

function view(added: string[]): ExeAccountView {
  const ig = (name: string, type: string, extra = {}) => ({
    name,
    type,
    team: false,
    title: "",
    comment: "",
    repositories: [],
    attachments: added.includes(name) ? ["tag:shelley-p"] : [],
    added: added.includes(name),
    reaches: added.includes(name),
    ...extra,
  });
  return {
    integration: "full-exe",
    tag: "shelley-p",
    vms: [{ vm_name: "host-vm", found: true, tags: [], tagged: added.length > 0 }],
    integrations: [
      ig("clickhouse", "http-proxy", { comment: "Analytics proxy" }),
      ig("claude", "llm", { title: "Anthropic" }),
      ig("ci", "http-proxy", { team: true, attachments: ["tag:buildkite"] }),
      ig("acme-web", "github", { repositories: ["acme/web"], attachments: ["vm:other"] }),
    ],
    error: "",
    manage_url: "https://exe.dev/integrations",
  };
}

// Mocks this server's exe.dev endpoints; changes records the account
// changes the editor asked for.
async function mockExeDev(page: Page, connected: { value: boolean }, restricted = false) {
  const changes: unknown[] = [];
  const added: string[] = [];
  await page.route("**/api/exe-dev-integrations", (route) =>
    route.fulfill({ json: { integrations: ["full-exe"], default: "full-exe" } }),
  );
  await page.route("**/api/github-repos", (route) =>
    route.fulfill({
      json: {
        repos: [{ integration: "host-gh", repository: "acme/host", team: true }],
        connect_url: "https://exe.dev/integrations/add?service=github",
      },
    }),
  );
  const refusal = "tag-scoped SSH keys cannot list integrations";
  await page.route("**/api/github-accounts?*", (route) =>
    route.fulfill({
      json: restricted
        ? { accounts: null, error: refusal, setup_url: setupURL }
        : { accounts: connected.value ? manyRepos() : [], error: "", setup_url: setupURL },
    }),
  );
  await page.route("**/api/projects/*/exe-account*", async (route) => {
    if (route.request().method() === "GET")
      return route.fulfill({
        json: restricted ? { ...view([]), integrations: [], error: refusal } : view(added),
      });
    const change = route.request().postDataJSON() as {
      action: string;
      name?: string;
      repository?: string;
    };
    changes.push(change);
    if (change.action === "add") added.push(change.name!);
    if (change.action === "github") {
      added.push(change.repository!.replace("/", "-"));
      return route.fulfill({
        json: { integration: change.repository!.replace("/", "-"), repository: change.repository },
      });
    }
    return route.fulfill({ status: 204 });
  });
  return changes;
}

// Opens project name's editor, from the drawer grouped by project.
async function openEditor(page: Page, name: string, workspace = true) {
  await page.goto("/");
  await page
    .getByRole("button", { name: workspace ? "Open tasks" : "Open conversations", exact: true })
    .click();
  if (!workspace) {
    await page.getByRole("button", { name: "Group conversations" }).click();
    await page
      .locator(".group-by-menu-item")
      .filter({ hasText: /^Project$/ })
      .click();
  }
  await page.getByRole("button", { name: `${name} project actions`, exact: true }).click();
  await page
    .getByTestId("project-actions-menu")
    .getByRole("menuitem", { name: "Edit project…", exact: true })
    .click();
  return page.getByRole("region", { name: "Edit project", exact: true });
}

async function newProject(request: import("@playwright/test").APIRequestContext) {
  const name = `GitHub ${randomUUID().slice(0, 8)}`;
  const created = await request.post("/api/projects", {
    headers,
    data: { name, instructions: "", cwd: "" },
  });
  expect(created.ok()).toBeTruthy();
  return name;
}

test("stock mode keeps the editor's host repositories, without the exe.dev account", async ({
  page,
  request,
}) => {
  const name = await newProject(request);
  const changes = await mockExeDev(page, { value: true });
  let accountReads = 0;
  page.on("request", (r) => {
    if (/\/api\/(github-accounts|projects\/[^/]+\/exe-account)/.test(r.url())) accountReads++;
  });
  const editor = await openEditor(page, name, false);
  await expect(editor.locator(".project-section-heading h3")).toHaveText([
    "Repositories",
    "Conversations run on",
    "Actions",
  ]);
  await expect(editor.locator(".project-choice", { hasText: "acme/host" })).toBeVisible();
  await expect(editor.getByTestId("project-integrations")).toHaveCount(0);
  expect(accountReads).toBe(0);
  expect(changes).toEqual([]);
});

test.describe("in workspace mode", () => {
  test.beforeEach(async ({ request }) => {
    const on = await request.post("/feature-flags", { data: { name: "workspace", value: true } });
    expect(on.ok()).toBe(true);
  });
  test.afterEach(async ({ request }) => {
    expect((await request.delete("/feature-flags", { data: { name: "workspace" } })).ok()).toBe(
      true,
    );
  });

  test("connect GitHub, find and add repositories and integrations", async ({ page, request }) => {
    const name = await newProject(request);
    const connected = { value: false };
    const changes = await mockExeDev(page, connected);
    const editor = await openEditor(page, name);
    // Where tasks run comes first, then GitHub, then the other integrations.
    await expect(editor.locator(".project-section-heading h3")).toHaveText([
      "Tasks run on",
      "GitHub repositories",
      "Integrations",
      "Actions",
    ]);

    // Without GitHub, the editor sends the user to exe.dev's sign-in, and
    // reloads when they come back.
    const connect = editor.getByRole("link", { name: "Connect GitHub" });
    await expect(connect).toHaveAttribute("href", setupURL);
    await connect.evaluate((a) => a.addEventListener("click", (e) => e.preventDefault()));
    await connect.click();
    connected.value = true;
    await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange")));
    const list = editor.getByTestId("github-repo-list");
    const rows = list.locator(".project-repo-row");
    await expect(rows).toHaveCount(50);
    await editor.getByRole("button", { name: "Show more (71 left)" }).click();
    await expect(rows).toHaveCount(100);

    const search = editor.getByLabel("Search repositories");
    await expect(search).toHaveAttribute("placeholder", "Search 121 repositories");
    await editor.getByLabel("Owner").selectOption("ann");
    await expect(rows).toHaveCount(30);
    await editor.getByLabel("Owner").selectOption("");
    await search.fill("web");
    await expect(rows).toHaveCount(1);
    await expect(rows.first()).toContainText(
      "Integration acme-web: adding attaches it to the project's VMs",
    );
    await rows.first().getByRole("button", { name: "Add" }).click();
    await expect(rows.first().getByRole("button", { name: "Remove" })).toBeVisible();
    await search.fill("acme/repo-007");
    await expect(rows.first()).toContainText("No integration yet: adding creates one");
    await rows.first().getByRole("button", { name: "Add" }).click();
    await search.fill("");
    // The project's repositories come first.
    await expect(rows.nth(0)).toContainText("acme/repo-007");
    await expect(rows.nth(1)).toContainText("acme/web");

    const integrations = editor.getByTestId("project-integrations");
    await expect(integrations.locator(".project-repo-row strong")).toHaveText([
      "ci",
      "claude",
      "clickhouse",
    ]);
    await integrations
      .locator(".project-repo-row", { hasText: "clickhouse" })
      .getByRole("button", { name: "Add" })
      .click();
    await expect(
      integrations.locator(".project-repo-row", { hasText: "clickhouse" }),
    ).toContainText("Added to the project's VMs");
    expect(changes).toEqual([
      expect.objectContaining({ action: "github", repository: "acme/web", mode: "host" }),
      expect.objectContaining({ action: "github", repository: "acme/repo-007" }),
      expect.objectContaining({ action: "add", name: "clickhouse", team: false }),
    ]);
    await page.setViewportSize({ width: 1100, height: 2000 });
    await page.screenshot({ path: "test-results/project-github.png" });

    await editor.getByRole("button", { name: "Save project", exact: true }).click();
    await expect(editor).toBeHidden();
    const { projects } = (await (await request.get("/api/workspace")).json()) as {
      projects: { name: string; repos: unknown[] }[];
    };
    expect(projects.find((p) => p.name === name)?.repos).toEqual([
      { integration: "acme-web", repository: "acme/web" },
      { integration: "acme-repo-007", repository: "acme/repo-007" },
    ]);
  });

  test("a restricted exe.dev integration explains itself and falls back", async ({
    page,
    request,
  }) => {
    const name = await newProject(request);
    await mockExeDev(page, { value: true }, true);
    const editor = await openEditor(page, name);
    await expect(editor.locator(".project-notice").first()).toContainText(
      "but through full-exe, tag-scoped SSH keys cannot list integrations",
    );
    await expect(editor.locator(".project-choice", { hasText: "acme/host" })).toBeVisible();
    await expect(
      editor.getByRole("link", { name: "Connect a GitHub repository on exe.dev" }),
    ).toBeVisible();
    await expect(editor.getByTestId("project-integrations")).toContainText(
      "can't list your account's integrations",
    );
    await page.setViewportSize({ width: 1100, height: 1400 });
    await page.screenshot({ path: "test-results/project-github-restricted.png" });
  });

  test("first-run onboarding starts a project for the picked repositories", async ({ page }) => {
    await mockExeDev(page, { value: true });
    // The first run: no projects besides Shelley's own.
    await page.route("**/api/workspace", async (route) => {
      const response = await route.fetch();
      const workspace = (await response.json()) as {
        projects: { name: string }[];
        shelley_project: string;
      };
      workspace.projects = workspace.projects.filter((p) => p.name === workspace.shelley_project);
      await route.fulfill({ response, json: workspace });
    });
    const saved: unknown[] = [];
    await page.route("**/api/projects", (route) => {
      saved.push(route.request().postDataJSON());
      return route.fulfill({ status: 201, json: route.request().postDataJSON() });
    });
    await page.route("**/api/projects/repo-007", (route) => {
      const project = route.request().postDataJSON() as { vm: object };
      saved.push(project);
      return route.fulfill({
        json: { ...project, vm: { ...project.vm, setup_conversation_id: "setup-id" } },
      });
    });
    await page.goto("/");
    await page.getByTestId("onboarding-github").click();
    const list = page.getByTestId("github-repo-list");
    await page.getByLabel("Search repositories").fill("acme/repo-007");
    await list.getByRole("button", { name: "Add" }).click();
    await page.getByTestId("github-start").click();
    await expect(page).toHaveURL(/\/c\/setup-id$/);
    expect(saved).toEqual([
      { name: "repo-007", instructions: "", cwd: "", actions: [], vm: null, repos: [] },
      expect.objectContaining({
        name: "repo-007",
        cwd: "/home/exedev/repo-007",
        vm: { base: "", shared_vm: "", integration: "full-exe", setup_conversation_id: "" },
        repos: [{ integration: "acme-repo-007", repository: "acme/repo-007" }],
      }),
    ]);
  });
});
