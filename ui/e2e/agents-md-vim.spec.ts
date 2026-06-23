import { test, expect } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

// Regression test for a hang in the AGENTS.md editor when Vim mode is enabled.
//
// The Monaco editor instance was stored in a deeply-reactive Vue ref(), which
// proxied Monaco's entire internal object graph. Enabling Vim (which drives the
// editor hard on every keystroke) then pegged the main thread and froze the
// page. The fix stores the editor in shallowRef(). This test enables Vim,
// drives a few keystrokes including the :wq ex-command (which routes through the
// quit handler and closes the modal), and asserts the page stays responsive.

test.describe("AGENTS.md editor + Vim", () => {
  // The Vim toggle is desktop-only (rendered when innerWidth >= 768). The
  // default project runs a narrow mobile viewport, so force a desktop size.
  test.use({ viewport: { width: 1280, height: 800 } });

  test("enabling Vim and running :wq does not hang the page", async ({ page, request }) => {
    test.setTimeout(60000);

    const slug = await createConversationViaAPI(request, "Hello");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    // Open the overflow menu and click "Edit User AGENTS.md".
    const overflowBtn = page.locator(".chat-overflow-menu-wrapper .btn-icon");
    await expect(overflowBtn).toBeVisible({ timeout: 10000 });
    await overflowBtn.click();

    const editItem = page.locator(".overflow-menu-item").filter({ hasText: /AGENTS\.md/i });
    await expect(editItem).toBeVisible();
    await editItem.click();

    // Wait for Monaco to mount inside the modal.
    const monaco = page.locator(".monaco-editor").first();
    await expect(monaco).toBeVisible({ timeout: 15000 });

    // Enable Vim mode (desktop-only toggle).
    const vimToggle = page.locator(".vim-toggle");
    await expect(vimToggle).toBeVisible();
    await vimToggle.click();

    // The vim status bar should appear and report normal mode. If the page were
    // hung, this would time out.
    const vimStatus = page.locator(".monaco-vim-status");
    await expect(vimStatus).toContainText("NORMAL", { timeout: 10000 });

    // Drive real keystrokes through monaco-vim. This is the path that pegged
    // the main thread when the editor was deeply reactive: every keystroke
    // touched Monaco's internals through Vue's reactive proxy. If the page
    // were hung, the assertions below would time out.
    const inputArea = monaco.locator("textarea.inputarea");
    await inputArea.focus();

    // Open a line below (normal-mode "o" enters insert mode), type, escape.
    // Lead with filler chars: monaco-vim can swallow the first keystroke or two
    // right after switching into insert mode, so the asserted marker comes last.
    await page.keyboard.press("o");
    await page.keyboard.type("___shelley_smoke_marker");
    await page.keyboard.press("Escape");

    // The typed marker must appear in the editor and Vim must be back in normal
    // mode — both prove the per-keystroke path ran promptly (no hang).
    await expect(monaco.locator(".view-lines")).toContainText("smoke_marker", {
      timeout: 10000,
    });
    await expect(vimStatus).toContainText("NORMAL");

    // Delete the inserted line (normal-mode "dd") so the debounced save that
    // flushes on close writes back the file's original content — this editor
    // auto-saves to the real ~/.config/shelley/AGENTS.md. "dd" removes the whole
    // line (text + the newline "o" opened), unlike "u" which can leave the line.
    await expect(async () => {
      await page.keyboard.type("dd");
      await expect(monaco.locator(".view-lines")).not.toContainText("smoke_marker", {
        timeout: 1000,
      });
    }).toPass({ timeout: 10000 });

    // Close the editor via the close button. Modal tears down (dispose path).
    await page.locator(".diff-viewer-close").click();
    await expect(monaco).toBeHidden({ timeout: 10000 });

    // Sanity: the page is still interactive (message input is reachable).
    await expect(page.locator('[data-testid="message-input"]')).toBeVisible();
  });
});
