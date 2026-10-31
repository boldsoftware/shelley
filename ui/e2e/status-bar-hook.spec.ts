import { test, expect, type Page } from "@playwright/test";
import { mkdirSync, renameSync, writeFileSync } from "fs";
import { dirname, join } from "path";
import {
  createConversationViaAPI,
  createConversationViaAPIWithDetails,
  git,
  initGitRepo,
  testWorkingDirectory,
  withTempDir,
} from "./helpers";

// The hook prints .status-bar.json from the conversation's directory, so it
// shows nothing for every other spec's conversations on the shared server.
const hookScript = `#!/bin/sh
[ ! -f .status-bar.json ] || cat .status-bar.json
`;

function installHook() {
  const hooks = join(dirname(testWorkingDirectory()), "home", ".config", "shelley", "hooks");
  mkdirSync(hooks, { recursive: true });
  const tmp = join(hooks, `.status-bar-${process.pid}`);
  writeFileSync(tmp, hookScript, { mode: 0o755 });
  // Replace, never rewrite in place: the server may be running it.
  renameSync(tmp, join(hooks, "status-bar"));
}

async function navigateWithinApp(page: Page, slug: string) {
  await page.evaluate((url) => {
    window.history.pushState({}, "", url);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, `/c/${slug}`);
}

async function useVMHostname(page: Page) {
  await page.addInitScript(() => {
    let init: Record<string, unknown> | undefined;
    Object.defineProperty(window, "__SHELLEY_INIT__", {
      configurable: true,
      get: () => init,
      set: (value) => {
        init = { ...value, is_exe_dev: true, hostname: "myvm.exe.xyz" };
      },
    });
  });
}

test("the status-bar hook shows the conversation's status and reruns after messages", async ({
  page,
  request,
}) => {
  test.skip(!!process.env.TEST_SERVER_URL, "installs a hook in the test server's home");
  installHook();
  await withTempDir("status-bar-hook-", async (cwd) => {
    const status = (s: object | string) =>
      writeFileSync(join(cwd, ".status-bar.json"), typeof s === "string" ? s : JSON.stringify(s));
    status({
      body: "feature-x `wip` http://localhost:8000/",
      bgcolor: "rgb(32, 74, 135)",
      color: "white",
    });
    const slug = await createConversationViaAPI(request, "Hello", { cwd });

    await useVMHostname(page);
    await page.goto(`/c/${slug}`);
    const segment = page.getByTestId("status-bar-hook");
    // VM-local URLs point at the exe.dev proxy, like links in chat.
    // Backticks stay backticks: the body isn't chat text.
    await expect(segment).toHaveText("feature-x `wip` https://myvm.exe.xyz:8000/");
    await expect(segment).toHaveCSS("background-color", "rgb(32, 74, 135)");
    await expect(segment).toHaveCSS("color", "rgb(255, 255, 255)");
    await expect(segment.getByRole("link")).toHaveAttribute("href", "https://myvm.exe.xyz:8000/");
    // Its own row, above the composer.
    const row = (await segment.boundingBox())!;
    const input = (await page.getByTestId("message-input").boundingBox())!;
    expect(row.y + row.height).toBeLessThanOrEqual(input.y);
    expect(row.width).toBeGreaterThanOrEqual(input.width);

    // A message reruns the hook.
    const send = async () => {
      await page.getByTestId("message-input").fill("Hello");
      await page.getByTestId("send-button").click();
    };
    status({ body: "feature-y" });
    await send();
    await expect(segment).toHaveText("feature-y");
    await expect(segment).not.toHaveAttribute("style", /background/);

    status("not json");
    await send();
    await expect(segment).toHaveText("Status hook failed");
    await segment.getByRole("button", { name: "Status hook failed" }).click();
    const errorDetails = page.getByRole("dialog", { name: "Status hook error" });
    await expect(errorDetails).toContainText("status-bar hook failed: invalid JSON output:");
    await page.keyboard.press("Escape");
    await expect(errorDetails).toBeHidden();

    status({ body: "feature-z", bgcolor: "rgb(32, 74, 135)" });
    await send();
    await expect(segment).toHaveText("feature-z");

    // Switching to another conversation keeps the row's place, blank, until
    // its hook answers, so the messages don't shift twice.
    await withTempDir("status-bar-hook-other-", async (other) => {
      writeFileSync(join(other, ".status-bar.json"), JSON.stringify({ body: "other-branch" }));
      const { conversationId: otherId, slug: otherSlug } =
        await createConversationViaAPIWithDetails(request, "Hello", { cwd: other });
      // Hold its answer in the browser, not the hook in the server, whose
      // timeout would be a race.
      let release!: () => void;
      const held = new Promise<void>((resolve) => (release = resolve));
      const statusURL = `**/api/conversation/${otherId}/status-bar`;
      const asked = page.waitForRequest(statusURL);
      await page.route(statusURL, async (route) => {
        await held;
        await route.continue();
      });
      await navigateWithinApp(page, otherSlug);
      await asked;
      await expect(segment).toHaveText(/^\s*$/);
      await expect(segment).not.toHaveAttribute("style", /background/);
      release();
      await expect(segment).toHaveText("other-branch");
      await page.unroute(statusURL);

      for (const empty of ["", {}, { body: null }, { body: [] }, { body: "   " }]) {
        status(empty);
        await navigateWithinApp(page, slug);
        await expect(segment).toHaveCount(0);
        await navigateWithinApp(page, otherSlug);
        await expect(segment).toHaveText("other-branch");
      }
    });
  });
});

test("status items preserve colors, native actions, links, and mobile scrolling", async ({
  page,
  request,
}) => {
  test.skip(!!process.env.TEST_SERVER_URL, "installs a hook in the test server's home");
  installHook();
  await withTempDir("status-bar-items-", async (cwd) => {
    initGitRepo(cwd);
    writeFileSync(join(cwd, "file.txt"), "initial\n");
    git(cwd, "add", "file.txt");
    git(cwd, "commit", "-m", "Initial file");
    const commit = git(cwd, "rev-parse", "HEAD");
    const status = (value: object) =>
      writeFileSync(join(cwd, ".status-bar.json"), JSON.stringify(value));
    status({
      bgcolor: "rgb(255, 255, 255)",
      color: "rgb(0, 0, 0)",
      body: [
        { body: "feature/long-branch-name", action: "open_git" },
        { body: "Review commit", action: "open_diff", args: { commit } },
        { body: "Predictable", action: "open_models" },
        {
          body: "Preview",
          action: "open_url",
          args: { url: "http://localhost:8000/preview" },
          bgcolor: "rgb(240, 240, 240)",
          color: "rgb(128, 0, 0)",
        },
      ],
    });
    const slug = await createConversationViaAPI(request, "Hello", { cwd });
    await useVMHostname(page);
    await page.emulateMedia({ colorScheme: "dark" });

    for (const width of [1280, 390]) {
      await page.setViewportSize({ width, height: 900 });
      await page.goto(`/c/${slug}`);
      const segment = page.getByTestId("status-bar-hook");
      const branch = segment.getByRole("button", { name: "feature/long-branch-name" });
      const model = segment.locator(".model-picker-inline");
      const link = segment.getByRole("link", { name: "Preview" });
      await expect(segment).toHaveCSS("background-color", "rgb(255, 255, 255)");
      await expect(branch).toHaveCSS("color", "rgb(0, 0, 0)");
      await branch.hover();
      await expect(branch).toHaveCSS("color", "rgb(0, 0, 0)");
      await expect(link).toHaveAttribute("href", "https://myvm.exe.xyz:8000/preview");
      await expect(link).toHaveAttribute("target", "_blank");
      await expect(link).toHaveAttribute("rel", "noopener noreferrer");
      await expect(link).toHaveCSS("background-color", "rgb(240, 240, 240)");
      await link.hover();
      await expect(link).toHaveCSS("color", "rgb(128, 0, 0)");

      await branch.focus();
      await page.keyboard.press("Tab");
      await page.keyboard.press("Shift+Tab");
      for (const target of [branch, model.locator(".p-select-label"), link]) {
        await target.focus();
        await expect(target).toBeFocused();
        await expect(target).toHaveCSS("outline-width", "2px");
        await expect(target).toHaveCSS("outline-style", "solid");
        await expect(target).toHaveCSS("outline-offset", "0px");
        await expect(target).toHaveCSS("box-shadow", "none");
        const bounds = await target.evaluate((control) => {
          const box = control.getBoundingClientRect();
          const row = control.closest('[data-testid="status-bar-hook"]')!.getBoundingClientRect();
          return { top: box.top - 2 - row.top, bottom: row.bottom - box.bottom - 2 };
        });
        expect(bounds.top).toBeGreaterThanOrEqual(-0.1);
        expect(bounds.bottom).toBeGreaterThanOrEqual(-0.1);
      }

      await model.hover();
      await expect(model).toHaveCSS("color", "rgb(0, 0, 0)");
      await model.click();
      const picker = page.locator(".model-picker-panel");
      await expect(picker).toBeVisible();
      await expect(picker.locator(".p-select-option")).not.toHaveCount(0);
      await picker.getByRole("button", { name: "Close", exact: true }).click();
      await expect(picker).toBeHidden();
      await expect(model).toHaveCSS("color", "rgb(0, 0, 0)");

      const diffRequest = page.waitForRequest((req) => {
        const url = new URL(req.url());
        return url.pathname === "/api/git/diffs" && url.searchParams.get("commit") === commit;
      });
      await segment.getByRole("button", { name: "Review commit" }).click();
      await diffRequest;
      const diff = page.locator(".diff-viewer-overlay");
      await expect(diff).toBeVisible();
      await diff.locator(".diff-viewer-close").click();
      await expect(diff).toBeHidden();

      await branch.click();
      const graph = page.locator(".git-graph-container");
      await expect(graph).toBeVisible();
      await graph.getByRole("button", { name: "Close", exact: true }).click();
      await expect(graph).toBeHidden();

      if (width < 768) {
        await expect(segment.locator(".status-hook-items")).toHaveCSS("flex-wrap", "nowrap");
        const scroll = await segment.evaluate((row) => {
          row.scrollLeft = 50;
          return {
            scrolled: row.scrollLeft,
            viewport: innerWidth,
            pageWidth: document.documentElement.scrollWidth,
          };
        });
        expect(scroll.scrolled).toBeGreaterThan(0);
        expect(scroll.pageWidth).toBe(scroll.viewport);
      }
    }

    for (const invalid of [
      { args: { commmit: commit }, error: "commmit" },
      { args: { commit: "HEAD~1" }, error: "full or abbreviated commit hash" },
    ]) {
      status({ body: [{ body: "Bad commit", action: "open_diff", args: invalid.args }] });
      await page.reload();
      const segment = page.getByTestId("status-bar-hook");
      await expect(segment).toHaveText("Status hook failed");
      await expect(page.locator(".diff-viewer-overlay")).toHaveCount(0);
      await segment.getByRole("button", { name: "Status hook failed" }).click();
      const errorDetails = page.getByRole("dialog", { name: "Status hook error" });
      await expect(errorDetails).toContainText(invalid.error);
      await expect(errorDetails).toBeInViewport({ ratio: 1 });
      await page.keyboard.press("Escape");
      await expect(errorDetails).toBeHidden();
      await expect(segment).toHaveText("Status hook failed");
    }
  });
});
