import { test, expect } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createConversationViaAPIWithDetails, git, initGitRepo, withTempDir } from "./helpers";

test("status branch is opt-in, persists, and follows the cwd worktree", async ({
  page,
  request,
}) => {
  try {
    await withTempDir("shelley-status-branch-", async (dir) => {
      const repo = join(dir, "repo");
      const worktree = join(dir, "worktree");
      const cwd = join(worktree, "nested");
      mkdirSync(repo);
      initGitRepo(repo);
      git(repo, "commit", "--allow-empty", "-m", "fixture");
      git(repo, "worktree", "add", "-b", "topic", worktree);
      mkdirSync(cwd);
      const { slug, conversationId } = await createConversationViaAPIWithDetails(
        request,
        "echo branch status",
        { cwd },
      );
      await page.setViewportSize({ width: 1280, height: 800 });
      await page.goto(`/c/${slug}`);
      const branch = page.locator(".status-readout-branch:visible");
      await expect(page.locator(".status-ready:visible")).toBeVisible();
      await expect(branch).toHaveCount(0);
      await page.keyboard.press("ControlOrMeta+k");
      await page.locator(".command-palette-input").fill("Show Git branch in status bar");
      await page.getByText("Show Git branch in status bar", { exact: true }).click();
      await expect(branch).toHaveText("topic");
      await page.reload();
      await expect(branch).toHaveText("topic");

      const name = "feature/status-bar-branch-indicator";
      await page.getByTestId("message-input").fill(`bash: git checkout -b ${name}`);
      await page.getByTestId("send-button").click();
      await expect(branch).toHaveText(name);
      await branch.hover();
      await expect(page.locator(".p-tooltip-text")).toContainText(name);
      await expect(page.locator(".p-tooltip-text")).toContainText(worktree);

      await page.setViewportSize({ width: 390, height: 800 });
      await expect(page.locator(".status-readout-branch")).toBeHidden();
      await page.setViewportSize({ width: 1280, height: 800 });
      await page.getByTestId("message-input").fill("bash: git checkout --detach");
      await page.getByTestId("send-button").click();
      await expect(branch).toHaveText(
        `detached@${git(worktree, "rev-parse", "--short=7", "HEAD")}`,
      );
      await expect(page.locator(".status-ready:visible")).toBeVisible();

      const response = await request.post(`/api/conversation/${conversationId}/cwd`, {
        data: { cwd: dir },
      });
      expect(response.ok()).toBe(true);
      await expect(page.locator(".status-readout-branch")).toHaveCount(0);
      expect(
        (await request.post(`/api/conversation/${conversationId}/cwd`, { data: { cwd } })).ok(),
      ).toBe(true);
      await expect(branch).toContainText("detached@");
      await page.keyboard.press("ControlOrMeta+k");
      await page.locator(".command-palette-input").fill("Hide Git branch in status bar");
      await page.getByText("Hide Git branch in status bar", { exact: true }).click();
      await expect(branch).toHaveCount(0);
      await page.reload();
      await expect(page.locator(".status-ready:visible")).toBeVisible();
      await expect(branch).toHaveCount(0);
    });
  } finally {
    expect(
      (await request.post("/settings", { data: { key: "show_git_branch", value: "false" } })).ok(),
    ).toBe(true);
  }
});

test("TOC shares the compact status row across desktop and mobile", async ({ page, request }) => {
  const { slug, conversationId } = await createConversationViaAPIWithDetails(
    request,
    "echo contents row",
  );
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto(`/c/${slug}`);
  const toc = page.getByRole("button", { name: "Conversation table of contents", exact: true });
  await expect(toc).toBeVisible();
  await toc.evaluate((el) => {
    el.dataset.mountIdentity = "original";
  });
  await expect(page.locator(".chat-nav-cluster .toc-button")).toHaveCount(0);

  // Block on a pipe, not a timer: the turn stays working until Stop is clicked.
  const dir = mkdtempSync(join(tmpdir(), "shelley-status-row-"));
  const pipe = join(dir, "gate");
  execFileSync("mkfifo", [pipe]);
  try {
    await page.getByTestId("message-input").fill(`bash: read -r line < ${pipe}`);
    await page.getByTestId("send-button").click();
    await expect(page.getByTestId("agent-thinking")).toBeVisible();
    for (const width of [
      1280, 900, 800, 700, 650, 600, 575, 550, 525, 500, 460, 420, 390, 360, 320,
    ]) {
      await page.setViewportSize({ width, height: 800 });
      await expect(async () => {
        const geometry = await page.evaluate(() => {
          const rect = (selector: string) => {
            const el = [...document.querySelectorAll<HTMLElement>(selector)].find(
              (e) => e.offsetWidth > 0,
            )!;
            if (!el) throw new Error(`Missing visible ${selector} at ${window.innerWidth}px`);
            const r = el.getBoundingClientRect();
            return {
              left: r.left,
              right: r.right,
              top: r.top,
              bottom: r.bottom,
              overflow: el.scrollWidth > el.clientWidth + 1,
            };
          };
          const text = document.querySelector<HTMLElement>(".animated-working")!;
          const textRect = text.offsetWidth > 0 ? rect(".animated-working") : null;
          return {
            toc: rect(".toc-button"),
            model: window.innerWidth >= 768 ? rect(".status-readout-model") : null,
            status: textRect,
            stop: rect(".status-stop-button"),
            row: rect(".status-bar-active"),
            tokens: window.innerWidth >= 768 ? rect(".context-usage-label-tokens") : null,
            input: rect(".message-textarea"),
            send: rect(".message-send-wrapper"),
            text: [...text.querySelectorAll('span[aria-hidden="true"]')]
              .filter((e) => e.getClientRects().length > 0)
              .map((e) => e.textContent)
              .join(""),
          };
        });
        expect(geometry.toc.left).toBeGreaterThanOrEqual(
          width <= 767 ? geometry.stop.right : geometry.model!.right,
        );
        const anchor = width <= 767 ? geometry.stop : geometry.tokens!;
        expect(
          Math.abs((geometry.toc.top + geometry.toc.bottom - anchor.top - anchor.bottom) / 2),
        ).toBeLessThan(2);
        expect(geometry.toc.right).toBeLessThanOrEqual(width);
        expect(geometry.row.overflow).toBe(false);
        if (width <= 767) {
          expect(geometry.status!.right - geometry.status!.left).toBe(1);
          expect(geometry.status!.bottom - geometry.status!.top).toBe(1);
          await expect(page.locator(".context-usage-label")).toBeHidden();
          await expect(page.locator(".chat-context-popup")).toBeHidden();
          expect(geometry.stop.right - geometry.stop.left).toBe(24);
          expect(geometry.send.bottom).toBeLessThanOrEqual(geometry.input.top);
          await expect(page.locator(".status-readout-cwd")).toBeHidden();
          await expect(page.locator(".status-readout-model")).toBeHidden();
          await expect(page.locator(".status-stop-button")).toHaveCSS("animation-name", "none");
        } else {
          expect(geometry.tokens!.overflow).toBe(false);
          expect(geometry.status).not.toBeNull();
          expect(geometry.status!.bottom - geometry.status!.top).toBeLessThan(24);
          expect(geometry.status!.right).toBeLessThanOrEqual(geometry.stop.left);
          expect(["Agent working...", "working...", "..."]).toContain(geometry.text);
        }
      }).toPass({ timeout: 5000 });
      await expect(page.getByTestId("agent-thinking")).toMatchAriaSnapshot(
        "- status: Agent working...",
      );
      await expect(toc).toHaveAttribute("data-mount-identity", "original");
      await toc.click();
      await expect(page.locator(".toc-popover")).toBeVisible();
      await page.keyboard.press("Escape");
      if (width === 390) {
        await page.getByRole("button", { name: "More options", exact: true }).click();
        const model = page.locator(".model-picker-menu");
        await expect(model).toHaveClass(/p-disabled/);
        await expect(model).toHaveCSS("opacity", "0.5");
        await expect(model.getByRole("combobox")).toHaveAttribute("aria-disabled", "true");
        await expect(model.getByRole("combobox")).toHaveAttribute(
          "aria-description",
          /switch models/,
        );
        await page.keyboard.press("Escape");
      }
    }
    // A narrow conversation pane on a desktop must shorten the label too.
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.locator(".main-content").evaluate((el) => {
      el.style.maxWidth = "320px";
    });
    await expect(page.locator(".working-prefix").first()).toBeHidden();
    await expect(page.locator(".working-word").first()).toBeHidden();
    await expect(page.locator(".animated-working .sr-only")).toBeVisible();
    await page.getByRole("button", { name: "Stop", exact: true }).click();
    await expect(page.getByTestId("agent-thinking")).toBeHidden();
    await expect(toc).toBeVisible();
    await page.setViewportSize({ width: 390, height: 800 });
    await expect(page.locator(".status-ready")).toBeHidden();
    await expect(page.locator(".status-stop-button")).toHaveCount(0);
    await expect(page.locator(".context-usage-label")).toBeHidden();
    await expect(page.locator(".status-readout-cwd-leaf")).toBeHidden();
    await expect(page.locator(".status-readout-cwd-path")).toBeHidden();
    for (const dark of [false, true]) {
      await page.emulateMedia({ colorScheme: dark ? "dark" : "light" });
      await expect(page.locator("html")).toHaveClass(dark ? /dark/ : /^(?!.*\bdark\b).*$/);
      await expect(page.locator(".status-ready")).toBeHidden();
      await expect(page.locator(".status-readout-sep-cwd")).toBeHidden();
    }
    expect((await request.post(`/api/conversation/${conversationId}/archive`)).ok()).toBe(true);
    await page.reload();
    await expect(page.getByTestId("message-input")).toBeHidden();
    await expect(page.locator(".toc-button")).toHaveCount(1);
    await expect(toc).toBeVisible();
  } finally {
    await request.post(`/api/conversation/${conversationId}/cancel`);
    rmSync(dir, { recursive: true, force: true });
  }
});

test("mobile composer keeps attachments and wrapped errors above the input", async ({
  page,
  request,
}) => {
  const { slug, conversationId } = await createConversationViaAPIWithDetails(
    request,
    "echo layout",
  );
  await page.setViewportSize({ width: 390, height: 800 });
  await page.route("**/api/upload", (route) =>
    route.fulfill({ json: { path: "/tmp/composer-layout.txt" } }),
  );
  await page.goto(`/c/${slug}`);
  await expect(page.getByTestId("message-input")).toBeVisible();
  await expect(page.locator(".context-usage-label")).toBeHidden();
  await expect(page.locator(".status-ready")).toBeHidden();

  for (const count of [1, 5]) {
    await page.locator('input[type="file"]').setInputFiles(
      Array.from({ length: count }, (_, i) => ({
        name: `attachment-${count}-${i}.txt`,
        mimeType: "text/plain",
        buffer: Buffer.from("layout fixture"),
      })),
    );
    await expect(page.locator(".message-attachment-uploading")).toHaveCount(0);
    await expect(page.locator(".message-attachment-ready")).toHaveCount(count === 1 ? 1 : 6);
    for (const width of [390, 320]) {
      await page.setViewportSize({ width, height: 800 });
      await expect(async () => {
        const layout = await page.evaluate(() => {
          const rect = (selector: string) =>
            document.querySelector(selector)!.getBoundingClientRect();
          return {
            controlsBottom: Math.max(
              rect(".message-controls-status-slot").bottom,
              rect(".message-send-wrapper").bottom,
            ),
            attachments: rect(".message-attachments").toJSON(),
            inputTop: rect(".message-textarea").top,
          };
        });
        expect(layout.attachments.top).toBeGreaterThanOrEqual(layout.controlsBottom);
        expect(layout.attachments.bottom).toBeLessThan(layout.inputTop);
        expect(layout.attachments.height).toBeGreaterThan(count === 1 ? 64 : 128);
      }).toPass();
    }
  }

  while (await page.locator(".message-attachment-remove").count()) {
    await page.locator(".message-attachment-remove").first().click();
  }
  await expect(page.getByTestId("message-attachments")).toHaveCount(0);
  const error =
    "The server could not accept this message because the requested model is unavailable. Please select another model and try sending the message again.";
  await page.route(`**/api/conversation/${conversationId}/chat`, (route) =>
    route.fulfill({ status: 503, contentType: "text/plain", body: error }),
  );
  await page.getByTestId("message-input").fill("keep my draft");
  await page.getByTestId("send-button").click();
  const errorMessage = page.locator(".message-controls-status-slot .status-error");
  await expect(errorMessage).toContainText(error);
  for (const width of [390, 320]) {
    await page.setViewportSize({ width, height: 800 });
    await expect(async () => {
      const layout = await page.evaluate(() => {
        const rect = (selector: string) =>
          document.querySelector(selector)!.getBoundingClientRect();
        return {
          error: rect(".status-error").toJSON(),
          formTop: rect(".message-input-form").top,
          inputTop: rect(".message-textarea").top,
        };
      });
      expect(layout.error.height).toBeGreaterThan(36);
      expect(layout.error.top).toBeGreaterThanOrEqual(layout.formTop);
      expect(layout.error.bottom).toBeLessThan(layout.inputTop);
    }).toPass();
  }
  await page.locator(".message-controls-status-slot .status-button-text").click();
  await expect(page.getByTestId("message-input")).toBeVisible();
  await expect(page.locator(".context-usage-label")).toBeHidden();
  await expect(page.locator(".status-ready")).toBeHidden();
  await expect(page.getByTestId("message-input")).toHaveValue("keep my draft");
});

test("context count and usage graphs stay desktop-only", async ({ page, request }) => {
  const { slug } = await createConversationViaAPIWithDetails(request, "echo desktop usage");
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto(`/c/${slug}`);
  const count = page.locator(".context-usage-label");
  await expect(count).toBeVisible();
  await count.click();
  const popup = page.locator(".chat-context-popup");
  await expect(popup).toBeVisible();
  await expect(popup.locator(".token-cost-graph-svg")).toBeVisible();
  for (const width of [767, 390, 320]) {
    await page.setViewportSize({ width, height: 800 });
    await expect(count).toBeHidden();
    await expect(popup).toBeHidden();
    await expect(
      page.getByRole("button", { name: "Conversation table of contents", exact: true }),
    ).toBeVisible();
  }
  await page.setViewportSize({ width: 768, height: 800 });
  await expect(count).toBeVisible();
  await count.click();
  await expect(popup).toBeVisible();
});
