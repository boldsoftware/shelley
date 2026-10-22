import { test, expect, type Locator, type Page } from "@playwright/test";
import { writeFileSync } from "node:fs";
import { join } from "node:path";
import {
  createConversationViaAPIWithDetails,
  git,
  initGitRepo,
  installServerLinks,
  openWorkspaceTool,
  withTempDir,
} from "./helpers";

async function expectLinkPopup(page: Page, link: Locator, url: string) {
  const popupPromise = page.waitForEvent("popup");
  await link.click();
  const popup = await popupPromise;
  await expect(popup).toHaveURL(url);
  await expect(popup.getByText("Local link fixture", { exact: true })).toBeVisible();
  return popup;
}

async function installLinkRoutes(page: Page) {
  for (const host of ["vm.example", "exe.example", "docs.example"]) {
    // Context routes also intercept the popup's first request; page routes do not.
    await page
      .context()
      .route(`https://${host}/**`, (route) =>
        route.fulfill({ contentType: "text/html", body: "<p>Local link fixture</p>" }),
      );
  }
}

for (const viewport of [
  { width: 320, height: 568 },
  { width: 393, height: 851 },
  { width: 768, height: 1024 },
  { width: 1280, height: 800 },
]) {
  test(`compact workspace toolbar and overflow work at ${viewport.width}px`, async ({
    page,
    request,
  }) => {
    await page.setViewportSize(viewport);
    const links = await installServerLinks(page);
    await installLinkRoutes(page);
    await withTempDir("shelley-toolbar-", async (cwd) => {
      initGitRepo(cwd);
      const file = join(cwd, "toolbar-fixture.txt");
      writeFileSync(file, "before toolbar edit\n");
      git(cwd, "add", ".");
      git(cwd, "commit", "-m", "Toolbar fixture commit");
      writeFileSync(file, "after toolbar edit\n");
      const { conversationId } = await createConversationViaAPIWithDetails(request, "Hello", {
        cwd,
      });
      const renamed = await request.post(`/api/conversation/${conversationId}/rename`, {
        data: {
          slug: `${viewport.width}-${conversationId}-very-long-conversation-title-that-must-not-crowd-the-toolbar`,
        },
      });
      expect(renamed.ok()).toBeTruthy();
      await page.goto(`/c/${conversationId}`);
      const header = page.locator(".header");
      const toolbar = header.locator(".chat-workspace-actions");
      const mobile = viewport.width < 768;
      const diffs = toolbar.getByRole("button", { name: "Diffs", exact: true });
      const gitGraph = toolbar.getByRole("button", { name: "Git Graph", exact: true });
      const terminal = toolbar.getByRole("button", { name: "Terminal", exact: true });
      const divider = toolbar.locator(".chat-workspace-divider");
      const vm = header.getByRole("link", { name: "vm.example", exact: true, includeHidden: true });
      const newConversation = header.getByRole("button", { name: "New Conversation", exact: true });
      const call = header.getByTestId("live-call-header");
      const more = header.getByRole("button", { name: "More options", exact: true });
      await expect(call).toBeEnabled();
      // Calling is a secondary action, not another filled primary button.
      await expect(call).toHaveCSS("background-color", "rgba(0, 0, 0, 0)");

      await expect(toolbar.getByRole("button", { includeHidden: true })).toHaveCount(3);
      await expect(divider).toHaveCount(1);
      if (mobile) await expect(divider).toBeHidden();
      else await expect(divider).toBeVisible();
      await expect(header.getByRole("link", { includeHidden: true })).toHaveCount(1);
      for (const name of ["Command menu", "Edit File…", "MCP Servers"]) {
        await expect(header.getByRole("button", { name, exact: true })).toHaveCount(0);
      }
      if (!mobile) {
        for (const button of [diffs, gitGraph, terminal]) {
          await expect(button).toHaveAttribute("title", /.+\(.+\)/);
        }
      }
      await expect(vm).toHaveAttribute("href", links[1].url);
      await expect(vm).toHaveAttribute("target", "_blank");
      await expect(vm).toHaveAttribute("rel", "noopener noreferrer");
      await expect(vm).toHaveAttribute("aria-label", links[1].title);
      await expect(vm).toHaveAttribute("title", links[1].title);

      // Long titles truncate; controls remain ordered, non-overlapping, and tappable.
      const titleBounds = await header.locator(".header-title").boundingBox();
      expect(titleBounds!.width).toBeGreaterThanOrEqual(mobile ? 96 : 32);
      if (mobile) {
        for (const button of [diffs, gitGraph, terminal, vm]) {
          await expect(button).toBeHidden();
        }
        await expect(call).toHaveAttribute("aria-label", "Start voice conversation");
      }
      await expect(newConversation).toBeVisible();
      let previousRight = 0;
      for (const control of mobile
        ? [newConversation, call, more]
        : [diffs, gitGraph, terminal, vm, call, newConversation, more]) {
        await expect(control).toBeInViewport();
        const bounds = (await control.boundingBox())!;
        expect(bounds.x).toBeGreaterThanOrEqual(previousRight);
        expect(bounds.x + bounds.width).toBeLessThanOrEqual(viewport.width);
        previousRight = bounds.x + bounds.width;
      }
      if (!mobile) {
        const vmBounds = (await vm.boundingBox())!;
        const terminalBounds = (await terminal.boundingBox())!;
        const dividerBounds = (await divider.boundingBox())!;
        expect(dividerBounds.x).toBeGreaterThanOrEqual(terminalBounds.x + terminalBounds.width);
        expect(dividerBounds.x + dividerBounds.width).toBeLessThanOrEqual(vmBounds.x);
      }
      for (const control of await header.locator("button:visible, a:visible").all()) {
        const bounds = (await control.boundingBox())!;
        expect(bounds.x).toBeGreaterThanOrEqual(0);
        expect(bounds.x + bounds.width).toBeLessThanOrEqual(viewport.width);
        if (mobile) {
          const minimumSize = viewport.width < 360 ? 32 : 36;
          expect(bounds.width).toBeGreaterThanOrEqual(minimumSize);
          expect(bounds.height).toBeGreaterThanOrEqual(minimumSize);
        }
      }
      if (!mobile) {
        const vmPopup = await expectLinkPopup(page, vm, links[1].url);
        expect(await vmPopup.evaluate(() => window.opener === null)).toBe(true);
        await vmPopup.close();
      }

      await more.click();
      const menu = page.locator(".chat-overflow-popover");
      await expect(menu).toBeVisible();
      const menuBounds = (await menu.boundingBox())!;
      expect(menuBounds.x).toBeGreaterThanOrEqual(0);
      expect(menuBounds.y).toBeGreaterThanOrEqual(0);
      expect(menuBounds.x + menuBounds.width).toBeLessThanOrEqual(viewport.width);
      expect(menuBounds.y + menuBounds.height).toBeLessThanOrEqual(viewport.height);
      for (const name of ["Command menu", "Edit File…", "MCP Servers"]) {
        await expect(menu.getByRole("button", { name: new RegExp(`^${name}`) })).toBeVisible();
      }
      for (const name of mobile
        ? ["New Conversation"]
        : ["Diffs", "Git Graph", "Terminal", "vm.example", "New Conversation"]) {
        await expect(menu.getByRole("button", { name: new RegExp(`^${name}`) })).toHaveCount(0);
      }
      if (mobile) {
        for (const name of ["Diffs", "Git Graph", "Terminal", "vm.example"]) {
          await expect(menu.getByRole("button", { name, exact: true })).toBeVisible();
        }
      }
      for (const name of ["Command menu", "Edit File…", "Archive Conversation"]) {
        const shortcut = menu
          .getByRole("button", { name: new RegExp(`^${name}`) })
          .locator(".overflow-menu-shortcut");
        if (mobile) await expect(shortcut).toBeHidden();
        else await expect(shortcut).toBeVisible();
      }
      const language = menu.getByRole("button", { name: /^Change Language/ });
      await language.scrollIntoViewIfNeeded();
      await expect(language).toBeInViewport();
      await page.keyboard.press("Escape");

      for (const link of mobile ? links : [links[0], links[2]]) {
        await more.click();
        const action = menu.getByRole("button", { name: link.title, exact: true });
        const popup = await expectLinkPopup(page, action, link.url);
        await popup.close();
        await expect(menu).toBeHidden();
      }

      await openWorkspaceTool(page, "Command menu");
      const commandSearch = page.locator(".command-palette-input");
      await expect(commandSearch).toBeVisible();
      await commandSearch.fill("notification");
      await expect(page.locator(".command-palette-item").first()).toBeVisible();
      await page.keyboard.press("Escape");
      await expect(commandSearch).toBeHidden();

      await more.click();
      await menu.getByRole("button", { name: "MCP Servers", exact: true }).click();
      const mcp = page.getByRole("dialog", { name: "MCP Servers", exact: true });
      await expect(mcp).toBeVisible();
      await expect(menu).toBeHidden();
      await mcp.getByRole("button", { name: "Close modal", exact: true }).click();
      await expect(mcp).toBeHidden();

      const backgroundCommand = `printf toolbar-background-${conversationId}`;
      if (viewport.width === 1280) {
        await page.getByTestId("message-input").fill(`!${backgroundCommand}`);
        await page.getByTestId("send-button").click();
        await expect(
          page.locator(`.terminal-panel-tab[title="${backgroundCommand}"]`),
        ).toBeVisible();
      }

      await openWorkspaceTool(page, "Terminal");
      await expect(page.locator(".terminal-panel")).toBeVisible();
      const activeTerminal = page.locator(
        '.terminal-panel-content [data-terminal-id][style*="display: block"]',
      );
      await expect(
        activeTerminal.getByRole("textbox", { name: "Terminal input", exact: true }),
      ).toBeVisible();
      const terminalId = await activeTerminal.getAttribute("data-terminal-id");
      expect(terminalId).toBeTruthy();
      const openedTerminal = page.locator(`[data-terminal-id="${terminalId}"]`);
      await page.getByRole("button", { name: "Close active terminal", exact: true }).click();
      await expect(openedTerminal).toHaveCount(0);

      if (viewport.width === 1280) {
        const backgroundTab = page.locator(`.terminal-panel-tab[title="${backgroundCommand}"]`);
        await expect(backgroundTab).toBeVisible();
        await backgroundTab.getByRole("button", { name: "Close terminal", exact: true }).click();
        await expect(backgroundTab).toHaveCount(0);
      }

      await openWorkspaceTool(page, "Diffs");
      const diff = page.locator(".diff-viewer-overlay");
      await expect(diff).toBeVisible();
      await expect(diff.locator("select.diff-viewer-select").first()).toHaveValue(
        "toolbar-fixture.txt",
      );
      await page.keyboard.press("Escape");
      await expect(diff).toBeHidden();

      await openWorkspaceTool(page, "Git Graph");
      const graph = page.locator(".git-graph-container");
      await expect(graph).toBeVisible();
      await expect(graph.locator(".git-graph-subject")).toContainText(["Toolbar fixture commit"]);
      await page.keyboard.press("Escape");
      await expect(graph).toBeHidden();

      await openWorkspaceTool(page, "Edit File…");
      const finder = page.getByRole("textbox", { name: "Filter files" });
      await expect(finder).toBeVisible();
      await finder.fill("toolbar-fixture");
      await expect(page.locator(".grp-row")).toHaveCount(1);
      await finder.press("Enter");
      const editor = page.getByRole("dialog", { name: `Edit ${file}`, exact: true });
      await expect(editor).toBeVisible();
      await expect(
        editor.locator(".view-line", { hasText: "after toolbar edit" }).first(),
      ).toBeVisible();
      await editor.getByRole("button", { name: "Close (Esc)", exact: true }).click();
      await expect(editor).toBeHidden();

      await newConversation.click();
      await expect(page).toHaveURL(/\/new$/);
      await expect(page.getByTestId("message-input")).toBeVisible();
      if (!mobile) {
        await expect(diffs).toBeVisible();
        await expect(gitGraph).toBeVisible();
        await expect(terminal).toBeVisible();
      }
      if (mobile) await expect(vm).toBeHidden();
      else await expect(vm).toBeVisible();
    });
  });

  test(`missing VM link keeps all configured links in overflow at ${viewport.width}px`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    const links = await installServerLinks(page, false);
    await installLinkRoutes(page);
    await page.goto("/new");
    const header = page.locator(".header");
    const workspace = header.locator(".chat-workspace-actions");
    await expect(workspace.getByRole("button", { includeHidden: true })).toHaveCount(3);
    await expect(workspace.getByRole("button")).toHaveCount(viewport.width < 768 ? 0 : 3);
    await expect(header.getByTestId("live-call-header")).toBeVisible();
    await expect(header.getByRole("link", { includeHidden: true })).toHaveCount(0);
    for (const link of links) {
      await header.getByRole("button", { name: "More options", exact: true }).click();
      const menu = page.locator(".chat-overflow-popover");
      for (const configured of links) {
        await expect(
          menu.getByRole("button", { name: configured.title, exact: true }),
        ).toBeVisible();
      }
      const popup = await expectLinkPopup(
        page,
        menu.getByRole("button", { name: link.title, exact: true }),
        link.url,
      );
      await popup.close();
      await expect(menu).toBeHidden();
    }
  });
}

test("Git Graph remains a direct desktop toolbar action", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto("/new");
  const graph = page.locator(".chat-workspace-actions").getByRole("button", {
    name: "Git Graph",
    exact: true,
  });
  await expect(graph).toBeVisible();
  await graph.click();
  await expect(page.locator(".git-graph-container")).toBeVisible();
});

test("Manage on exe.dev opens this VM's management page", async ({ page }) => {
  await page.goto("/new");
  const init = await page.evaluate(() => window.__SHELLEY_INIT__!);
  test.skip(!init.is_exe_dev, "Management links are only provided on exe.dev");
  const url = `https://exe.dev/vm/${init.hostname!.split(".")[0]}`;
  await page
    .context()
    .route(url, (route) =>
      route.fulfill({ contentType: "text/html", body: "<h1>VM management</h1>" }),
    );
  await page.getByRole("button", { name: "More options", exact: true }).click();
  const menu = page.locator(".chat-overflow-popover");
  await expect(menu.getByRole("button", { name: "Back to exe.dev", exact: true })).toHaveCount(0);
  const manage = menu.getByRole("button", { name: "Manage on exe.dev", exact: true });
  await expect(manage).toBeVisible();
  const popupPromise = page.waitForEvent("popup");
  await manage.click();
  const popup = await popupPromise;
  await expect(popup).toHaveURL(url);
  await expect(popup.getByRole("heading", { name: "VM management" })).toBeVisible();
  await expect(menu).toBeHidden();
  await popup.close();
});
