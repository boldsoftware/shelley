import { expect, test, type Page } from "@playwright/test";

// A page of an app on another site that includes embed.js; beforeEach lets
// it frame Shelley.
const APP = "http://app.localhost/";
// A new pane loads all of Shelley, which takes a while on a busy machine.
const loads = { timeout: 20_000 };

type AppOptions = {
  app?: string;
  hash?: string;
  html?: string;
  head?: string;
  body?: string;
  csp?: string;
};

async function openApp(page: Page, baseURL: string | undefined, opts: AppOptions = {}) {
  const app = opts.app ?? APP;
  // Chrome takes the routed page for a public one, which may reach Shelley
  // on localhost only with the user's permission.
  if (test.info().project.name === "chromium") {
    await page.context().grantPermissions(["local-network-access"], { origin: app });
  }
  // app/framed is the same page, bare.
  await page.context().route(`${app}**`, (route) => {
    const o = route.request().url().endsWith("/framed") ? {} : opts;
    return route.fulfill({
      contentType: "text/html",
      headers: o.csp ? { "Content-Security-Policy": o.csp } : {},
      body: `<!doctype html><html ${o.html ?? ""}><meta name="viewport" content="width=device-width">
<title>App</title>${o.head ?? ""}<h1>App</h1>${o.body ?? ""}
<script src="${baseURL}/embed.js" defer></script>`,
    });
  });
  await page.goto(app + (opts.hash ?? ""));
  // The deferred script has run.
  await expect(page.locator("shelley-embed")).toBeAttached();
  return {
    open: page.getByRole("button", { name: "Edit with Shelley", exact: true }),
    hide: page.getByRole("button", { name: "Hide this button", exact: true }),
    close: page.getByRole("button", { name: "Close", exact: true }),
    input: page.frameLocator("iframe[title=Shelley]").getByTestId("message-input"),
    margin: () => page.evaluate(() => getComputedStyle(document.documentElement).marginRight),
  };
}

test.beforeEach(async ({ request }) => {
  const res = await request.post("/api/embed-origins", { data: { origin: APP } });
  expect(res.ok()).toBe(true);
});

test.describe("embed.js on a wide page", () => {
  test.use({ viewport: { width: 1200, height: 800 } });

  test("docks Shelley on the right from a button at the top right", async ({ page, baseURL }) => {
    const { open, hide, close, input, margin } = await openApp(page, baseURL);
    await expect(open).toBeVisible();
    await expect(open.locator("svg")).toBeVisible();
    const hideBox = (await hide.boundingBox())!;
    expect(hideBox.y).toBeLessThan(24);
    expect(1200 - (hideBox.x + hideBox.width)).toBeLessThan(24);

    await expect(open).toHaveAttribute("aria-expanded", "false");
    await open.click();
    await expect(input).toBeVisible(loads);
    await expect(close).toHaveAttribute("aria-expanded", "true");
    // The page makes room for the pane, and the button keeps to its top
    // right, now a Close button.
    expect(await margin()).toBe("480px");
    await expect(hide).toBeHidden();
    const closeBox = (await close.boundingBox())!;
    expect(closeBox.y).toBeLessThan(24);
    expect(closeBox.x + closeBox.width).toBeLessThanOrEqual(1200 - 480);

    // The pane stays open across loads, until closed.
    await page.reload();
    await expect(input).toBeVisible(loads);
    await close.click();
    await expect(page.locator("iframe[title=Shelley]")).toBeHidden();
    await expect(open).toBeVisible();
    expect(await margin()).toBe("0px");
  });

  test("keeps the page's own margin, and docks only on screen", async ({ page, baseURL }) => {
    const { open, close, input, margin } = await openApp(page, baseURL, {
      html: 'style="margin-right: 7px"',
    });
    expect(await margin()).toBe("7px");
    await open.click();
    await expect(input).toBeVisible(loads);
    expect(await margin()).toBe("480px");
    await page.emulateMedia({ media: "print" });
    expect(await margin()).toBe("7px");
    await expect(close).toBeHidden();
    await page.emulateMedia({ media: "screen" });
    await close.click();
    expect(await margin()).toBe("7px");
  });

  test("works under a strict CSP with Trusted Types", async ({ page, baseURL }) => {
    const { open, input } = await openApp(page, baseURL, {
      csp: `default-src 'none'; script-src ${baseURL}; frame-src ${baseURL}; style-src 'self'; require-trusted-types-for 'script'`,
    });
    const box = (await open.boundingBox())!;
    expect(box.y).toBeLessThan(24);
    expect(box.x).toBeGreaterThan(1200 - 240);
    await open.click();
    await expect(input).toBeVisible(loads);
  });

  test("hides the button until #shelley brings it back", async ({ page, baseURL }) => {
    const { open, hide, close, input } = await openApp(page, baseURL);
    await hide.click();
    await expect(open).toBeHidden();
    await expect(page.getByRole("status")).toHaveText(/#shelley/);
    await page.reload();
    await expect(page.locator("shelley-embed")).toBeAttached();
    await expect(open).toBeHidden();

    // #shelley shows it again, opens the pane, and leaves the URL.
    await page.evaluate(() => (location.hash = "shelley"));
    await expect(input).toBeVisible(loads);
    await expect(close).toBeVisible();
    expect(page.url()).toBe(APP);
    await page.reload();
    await expect(input).toBeVisible(loads);
  });

  test("#shelley on the first load opens the pane", async ({ page, baseURL }) => {
    const { input } = await openApp(page, baseURL, { hash: "#shelley" });
    await expect(input).toBeVisible(loads);
    expect(page.url()).toBe(APP);
  });

  test("runs once, from the head without defer, too", async ({ page, baseURL }) => {
    const { open, input } = await openApp(page, baseURL, {
      head: `<script src="${baseURL}/embed.js"></script>`,
    });
    await expect(page.locator("shelley-embed")).toHaveCount(1);
    await open.click();
    await expect(input).toBeVisible(loads);
  });

  test("does nothing in a frame", async ({ page, baseURL }) => {
    await openApp(page, baseURL, { body: `<iframe src="${APP}framed"></iframe>` });
    const framed = page.frameLocator("iframe[src$=framed]");
    await expect(framed.locator("h1")).toBeVisible();
    await expect(page.locator("shelley-embed")).toHaveCount(1);
    // embed.js has loaded in the frame, too.
    const frame = page.frames().find((f) => f.url().endsWith("/framed"))!;
    await expect
      .poll(() =>
        frame.evaluate(
          () =>
            document.readyState === "complete" &&
            performance.getEntriesByType("resource").some((e) => e.name.endsWith("/embed.js")),
        ),
      )
      .toBe(true);
    await expect(framed.locator("shelley-embed")).toHaveCount(0);
  });

  test("leaves the page's own #shelley alone, and offers #shelley-embed", async ({
    page,
    baseURL,
  }) => {
    const { open, hide, input } = await openApp(page, baseURL, {
      hash: "#shelley",
      body: '<h2 id="shelley">Shelley</h2>',
    });
    await expect(open).toBeVisible();
    await expect(page.locator("iframe[title=Shelley]")).toHaveCount(0);
    expect(page.url()).toBe(APP + "#shelley");

    await hide.click();
    await expect(page.getByRole("status")).toHaveText(/#shelley-embed /);
    await page.evaluate(() => (location.hash = "shelley-embed"));
    await expect(input).toBeVisible(loads);
    expect(page.url()).toBe(APP);
  });
});

test.describe("embed.js on a narrow page", () => {
  test.use({ viewport: { width: 390, height: 800 } });

  test("keeps the button above the pane", async ({ page, baseURL }) => {
    const { open, close, input, margin } = await openApp(page, baseURL);
    await open.click();
    await expect(input).toBeVisible(loads);
    // The pane covers the page, which keeps its width.
    expect(await margin()).toBe("0px");
    const closeBox = (await close.boundingBox())!;
    const frameBox = (await page.locator("iframe[title=Shelley]").boundingBox())!;
    expect(closeBox.y + closeBox.height).toBeLessThanOrEqual(frameBox.y);
    expect(390 - (closeBox.x + closeBox.width)).toBeLessThan(24);
  });
});

test("the command palette lists the sites that may open Shelley", async ({ page, baseURL }) => {
  // Each browser's own site, as they share Shelley.
  const app = `http://${test.info().project.name}.localhost`;
  await page.goto("/new");
  await expect(page.getByTestId("message-input")).toBeVisible(loads);
  await page.keyboard.press("ControlOrMeta+k");
  await page.locator(".command-palette-input").fill("edit with shelley");
  await page.locator(".command-palette-item").first().click();
  const modal = page.locator(".embed-modal");
  await expect(modal.locator(".modal-title")).toHaveText("Edit with Shelley");
  await expect(modal.getByTestId("embed-snippet")).toHaveText(
    `<script src="${baseURL}/embed.js" defer></script>`,
  );
  const sites = modal.getByRole("list", { name: "Sites whose pages can open Shelley" });
  await expect(sites).toContainText("http://app.localhost");

  const site = modal.getByRole("textbox", { name: "Sites whose pages can open Shelley" });
  const add = modal.getByRole("button", { name: "Add", exact: true });
  await site.fill("app.example.com");
  await add.click();
  await expect(modal.getByRole("alert")).toContainText("as https://app.example.com");
  await site.fill(app.toUpperCase() + "/");
  await add.click();
  await expect(sites.getByText(app, { exact: true })).toBeVisible();
  await expect(site).toHaveValue("");
  await expect(modal.getByRole("alert")).toHaveCount(0);

  const { open, input } = await openApp(page, baseURL, { app: app + "/" });
  await open.click();
  await expect(input).toBeVisible(loads);

  await page.goto("/new");
  await page.keyboard.press("ControlOrMeta+k");
  await page.locator(".command-palette-input").fill("edit with shelley");
  await page.locator(".command-palette-item").first().click();
  await modal.getByRole("button", { name: `Remove ${app}` }).click();
  await expect(sites.getByText(app, { exact: true })).toHaveCount(0);
  const csp = (await page.request.get("/")).headers()["content-security-policy"];
  expect(csp).toContain("http://app.localhost");
  expect(csp).not.toContain(app);
});
