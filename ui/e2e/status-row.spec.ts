import { test, expect } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createConversationViaAPIWithDetails } from "./helpers";

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
            cwd: rect(".status-readout-cwd"),
            model: rect(".status-readout-model"),
            status: textRect,
            stop: rect(".status-stop-button"),
            row: rect(".status-bar-active"),
            tokens: rect(".context-usage-label-tokens"),
            input: rect(".message-textarea"),
            send: rect(".message-send-wrapper"),
            text: [...text.querySelectorAll('span[aria-hidden="true"]')]
              .filter((e) => e.getClientRects().length > 0)
              .map((e) => e.textContent)
              .join(""),
          };
        });
        expect(geometry.toc.left).toBeGreaterThanOrEqual(
          width <= 767 ? geometry.cwd.right : geometry.model.right,
        );
        expect(
          Math.abs(
            (geometry.toc.top + geometry.toc.bottom - geometry.model.top - geometry.model.bottom) /
              2,
          ),
        ).toBeLessThan(2);
        expect(geometry.toc.right).toBeLessThanOrEqual(width);
        expect(geometry.row.overflow).toBe(false);
        expect(geometry.tokens.overflow).toBe(false);
        if (width <= 767) {
          expect(geometry.status!.right - geometry.status!.left).toBe(1);
          expect(geometry.status!.bottom - geometry.status!.top).toBe(1);
          expect(geometry.stop.right).toBeLessThanOrEqual(geometry.tokens.left);
          expect(geometry.stop.right - geometry.stop.left).toBe(24);
          expect(geometry.send.bottom).toBeLessThanOrEqual(geometry.input.top);
          expect(geometry.cwd.left - geometry.model.right).toBeLessThan(16);
        } else {
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
    await expect(page.locator(".status-ready")).toBeVisible();
    await expect(page.locator(".status-readout-cwd-leaf")).toBeVisible();
    await expect(page.locator(".status-readout-cwd-path")).toBeHidden();
    for (const dark of [false, true]) {
      await page.emulateMedia({ colorScheme: dark ? "dark" : "light" });
      await expect(page.locator("html")).toHaveClass(dark ? /dark/ : /^(?!.*\bdark\b).*$/);
      const dot = await page.locator(".status-ready").evaluate((el) => {
        const style = getComputedStyle(el, "::before");
        return {
          width: style.width,
          height: style.height,
          left: style.left,
          top: style.top,
          position: style.position,
          background: style.backgroundColor,
          opacity: style.opacity,
        };
      });
      expect(dot).toEqual({
        width: "8px",
        height: "8px",
        left: "8px",
        top: "13px",
        position: "absolute",
        background: dark ? "rgb(107, 114, 128)" : "rgb(156, 163, 175)",
        opacity: "1",
      });
      await expect(page.locator(".status-readout-sep-cwd")).toBeVisible();
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
  await expect(page.locator(".status-ready")).toBeVisible();

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
  await expect(page.locator(".status-ready")).toBeVisible();
  await expect(page.getByTestId("message-input")).toHaveValue("keep my draft");
});
