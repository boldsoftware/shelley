import { test, expect, type Page } from "@playwright/test";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createConversationViaAPI } from "./helpers";

const tempDirs: string[] = [];
function previewTempDir(prefix: string) {
  const dir = mkdtempSync(join(tmpdir(), prefix));
  tempDirs.push(dir);
  return dir;
}

test.afterEach(() => {
  for (const dir of tempDirs.splice(0)) rmSync(dir, { recursive: true, force: true });
});

async function openFile(page: Page, name: string) {
  await page.keyboard.press("ControlOrMeta+P");
  const finder = page.locator(".grp-filter");
  await expect(finder).toBeVisible();
  await finder.fill(name);
  await expect(page.locator(".grp-row").first()).toContainText(name);
  await finder.press("Enter");
  const modal = page.getByRole("dialog", {
    name: new RegExp(`Edit .*${name.replace(".", "\\.")}`),
  });
  await expect(modal.locator(".monaco-editor")).toBeVisible({ timeout: 15000 });
  return modal;
}

test.describe("Markdown file editor preview", () => {
  test.use({ viewport: { width: 1280, height: 800 } });

  test("renders tables and pending edits without running file contents", async ({
    page,
    request,
  }) => {
    const dir = previewTempDir("shelley-md-preview-");
    writeFileSync(
      join(dir, "notes.md"),
      [
        "# Release notes",
        "",
        "| Feature | Status |",
        "| --- | --- |",
        "| Preview | Ready |",
        "",
        "```js",
        "window.__previewExecuted = true;",
        "```",
        "",
        '<img src="/missing-image" onerror="window.__previewExecuted = true">',
        '<svg onload="window.__previewExecuted = true"></svg>',
        '<iframe srcdoc="<script>window.__previewExecuted = true</script>"></iframe>',
        "<script>window.__previewExecuted = true</script>",
        '<div class="diff-viewer-overlay">spoofed overlay</div>',
      ].join("\n"),
    );

    await page.addInitScript(() => {
      (window as unknown as Record<string, unknown>).__previewExecuted = false;
    });
    const slug = await createConversationViaAPI(request, "Hello", { cwd: dir });
    await page.goto(`/c/${slug}`);
    const modal = await openFile(page, "notes.md");

    const previewButton = modal.getByRole("button", { name: "Preview mode" });
    await expect(previewButton).toBeVisible();
    await page.keyboard.press("ControlOrMeta+Alt+P");
    const preview = modal.getByRole("region", { name: "Markdown preview" });
    await expect(preview).toBeVisible();
    await expect(preview).toBeFocused();
    await expect(preview.getByRole("heading", { name: "Release notes" })).toBeVisible();
    await expect(preview.getByRole("table").getByRole("cell", { name: "Ready" })).toBeVisible();
    await expect(preview.locator("pre code")).toContainText("window.__previewExecuted = true;");
    await expect(preview.locator("script, iframe, img, [onerror], [onload]")).toHaveCount(0);
    await expect(preview.locator(".diff-viewer-overlay")).toHaveCount(0);
    await expect(preview).toContainText("spoofed overlay");
    await expect(preview.locator("pre > code")).toHaveClass(/language-js/);
    // The UI's own code-copy button has an SVG icon; untrusted file SVG
    // markup must still be removed.
    await expect(preview.locator(".markdown-content > svg")).toHaveCount(0);
    expect(
      await page.evaluate(() => (window as unknown as Record<string, unknown>).__previewExecuted),
    ).toBe(false);

    // Return to Monaco, change the file, and preview again. No save/reload
    // should be necessary to see the current editor buffer.
    await page.keyboard.press("ControlOrMeta+Shift+K");
    const input = modal.locator(".monaco-editor textarea.inputarea");
    await input.focus();
    await page.keyboard.press("ControlOrMeta+End");
    await page.keyboard.type("\n\n## Unsaved view\n\nA fresh line.");
    await previewButton.click();
    await expect(preview.getByRole("heading", { name: "Unsaved view" })).toBeVisible();
    await expect(preview).toContainText("A fresh line.");
    await modal.getByRole("button", { name: "Edit mode" }).click();
    await expect(modal.locator(".view-lines")).toContainText("Unsaved view");
  });

  test("keeps preview exclusive to .md paths", async ({ page, request }) => {
    const dir = previewTempDir("shelley-plain-preview-");
    writeFileSync(join(dir, "notes.txt"), "# This is plain text\n");
    const slug = await createConversationViaAPI(request, "Hello", { cwd: dir });
    await page.goto(`/c/${slug}`);
    const modal = await openFile(page, "notes.txt");
    await expect(modal.getByRole("button", { name: "Preview mode" })).toHaveCount(0);
    await expect(modal.getByRole("button", { name: "Split view" })).toHaveCount(0);
    await page.keyboard.press("ControlOrMeta+Shift+K");
    await expect(modal.getByRole("region", { name: "Markdown preview" })).toHaveCount(0);
    await expect(modal.locator(".monaco-editor")).toBeVisible();
  });

  test("keeps Monaco's delete-line shortcut while the editor has focus", async ({
    page,
    request,
  }) => {
    const dir = previewTempDir("shelley-md-shortcut-");
    writeFileSync(join(dir, "keys.md"), "# Keep\nRemove this line\nKeep this too\n");
    const slug = await createConversationViaAPI(request, "Hello", { cwd: dir });
    await page.goto(`/c/${slug}`);
    const modal = await openFile(page, "keys.md");
    await modal.locator(".monaco-editor textarea.inputarea").focus();
    await page.keyboard.press("ControlOrMeta+Home");
    await page.keyboard.press("ArrowDown");
    await page.keyboard.press("ControlOrMeta+Shift+K");
    await expect(modal.locator(".view-lines")).not.toContainText("Remove this line");
    await expect(modal.getByRole("region", { name: "Markdown preview" })).toHaveCount(0);
    await page.keyboard.press("ControlOrMeta+Alt+P");
    const preview = modal.getByRole("region", { name: "Markdown preview" });
    await expect(preview).toBeVisible();
    await page.keyboard.press("ControlOrMeta+Shift+K");
    await expect(modal.locator(".monaco-editor")).toBeVisible();
    await expect(modal.locator(".view-lines")).not.toContainText("Remove this line");
  });

  test("leaves shortcuts to the stacked command palette", async ({ page, request }) => {
    const dir = previewTempDir("shelley-preview-palette-");
    writeFileSync(join(dir, "notes.md"), "# A note\n");
    const slug = await createConversationViaAPI(request, "Hello", { cwd: dir });
    await page.goto(`/c/${slug}`);
    const modal = await openFile(page, "notes.md");
    await modal.getByRole("button", { name: "Preview mode" }).click();
    await page.keyboard.press("ControlOrMeta+k");
    const palette = page.locator(".command-palette-input");
    await expect(palette).toBeVisible();
    await page.keyboard.press("ControlOrMeta+Alt+P");
    await expect(modal.getByRole("region", { name: "Markdown preview" })).toBeVisible();
    await expect(palette).toBeFocused();
  });

  test("also previews AGENTS.md without a comment toggle", async ({ page, request }) => {
    const slug = await createConversationViaAPI(request, "Hello");
    await page.goto(`/c/${slug}`);
    await page.locator(".chat-overflow-menu-wrapper .btn-icon").click();
    await page
      .locator(".overflow-menu-item")
      .filter({ hasText: /Edit User AGENTS\.md/ })
      .click();
    const modal = page.getByRole("dialog", { name: "Edit AGENTS.md" });
    await expect(modal.locator(".monaco-editor")).toBeVisible();
    await expect(modal.getByRole("button", { name: "Comment mode" })).toHaveCount(0);
    await modal.getByRole("button", { name: "Preview mode" }).click();
    await expect(modal.getByRole("region", { name: "Markdown preview" })).toBeVisible();
  });

  test("offers a live side-by-side editor and preview", async ({ page, request }) => {
    const dir = previewTempDir("shelley-split-preview-");
    writeFileSync(
      join(dir, "draft.md"),
      "# Draft\n\n| Item | Done |\n| --- | --- |\n| One | yes |\n\n```js\nconst x = 42;\n```\n",
    );
    const slug = await createConversationViaAPI(request, "Hello", { cwd: dir });
    await page.goto(`/c/${slug}`);
    const modal = await openFile(page, "draft.md");
    await modal.getByRole("button", { name: "Split view" }).click();
    const preview = modal.getByRole("region", { name: "Markdown preview" });
    await expect(preview.getByRole("table")).toBeVisible();
    const editorPane = modal.locator(".diff-viewer-editor");
    const editBox = await editorPane.boundingBox();
    const previewBox = await preview.boundingBox();
    expect(editBox).not.toBeNull();
    expect(previewBox).not.toBeNull();
    expect(Math.abs(editBox!.x + editBox!.width - previewBox!.x)).toBeLessThan(3);
    await expect(modal.locator(".monaco-editor textarea.inputarea")).toBeFocused();
    await page.keyboard.press("ControlOrMeta+End");
    await page.keyboard.type("\n\n## New while typing");
    await expect(preview.getByRole("heading", { name: "New while typing" })).toBeVisible();
    await modal.getByRole("button", { name: "Preview mode" }).click();
    await expect(editorPane).toBeHidden();
    await expect(preview.getByRole("heading", { name: "New while typing" })).toBeVisible();
    await expect(preview.locator("pre > code")).toHaveAttribute(
      "data-shelley-code-highlight",
      /^(js|javascript)$/,
    );
    await page.keyboard.press("ControlOrMeta+Shift+K");
    await expect(editorPane).toBeVisible();
    await expect(preview).toBeVisible();
    await page.keyboard.press("ControlOrMeta+Alt+P");
    await expect(editorPane).toBeHidden();
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(modal.getByRole("button", { name: "Split view" })).toHaveCount(0);
    await page.keyboard.press("ControlOrMeta+Alt+P");
    await expect(editorPane).toBeVisible();
    await expect(preview).toHaveCount(0);
  });

  test("supports Vim scrolling and returning to edit", async ({ page, request }) => {
    const dir = previewTempDir("shelley-vim-preview-");
    writeFileSync(join(dir, "long.md"), "# Long read\n\n" + "Paragraph of text.\n\n".repeat(100));
    const slug = await createConversationViaAPI(request, "Hello", { cwd: dir });
    await page.goto(`/c/${slug}`);
    const modal = await openFile(page, "long.md");

    await modal.locator(".vim-toggle").click();
    await expect(modal.locator(".monaco-vim-status")).toContainText("NORMAL");
    await modal.getByRole("button", { name: "Preview mode" }).click();
    const preview = modal.getByRole("region", { name: "Markdown preview" });
    await expect(preview).toBeFocused();
    await preview.evaluate((el) =>
      el.dispatchEvent(new KeyboardEvent("keydown", { key: "o", metaKey: true, bubbles: true })),
    );
    await expect(preview).toBeVisible();
    await page.keyboard.press("G");
    const atBottom = await preview.evaluate((el) => el.scrollTop);
    expect(atBottom).toBeGreaterThan(0);
    await page.keyboard.press("g");
    await page.keyboard.press("g");
    expect(await preview.evaluate((el) => el.scrollTop)).toBe(0);
    await page.keyboard.press("j");
    expect(await preview.evaluate((el) => el.scrollTop)).toBeGreaterThan(0);
    await page.keyboard.press("k");
    expect(await preview.evaluate((el) => el.scrollTop)).toBe(0);
    await page.keyboard.press("Control+d");
    expect(await preview.evaluate((el) => el.scrollTop)).toBeGreaterThan(0);
    await page.keyboard.press("i");
    await expect(modal.locator(".monaco-editor")).toBeVisible();
    await expect(modal.locator(".monaco-editor textarea.inputarea")).toBeFocused();
  });

  test("preserves an unfinished comment before entering preview", async ({ page, request }) => {
    const dir = previewTempDir("shelley-md-comment-preview-");
    writeFileSync(join(dir, "draft.md"), "# Keep this note\n");
    const slug = await createConversationViaAPI(request, "Hello", { cwd: dir });
    await page.goto(`/c/${slug}`);
    const modal = await openFile(page, "draft.md");
    await modal.getByRole("button", { name: "Comment mode" }).click();
    await modal.locator(".view-line", { hasText: "Keep this note" }).click();
    const dialog = page.locator(".diff-viewer-comment-dialog");
    await expect(dialog).toBeVisible();
    await dialog.locator(".diff-viewer-comment-input").fill("unfinished comment");
    await expect(modal.getByRole("button", { name: "Preview mode" })).toBeDisabled();
    await page.keyboard.press("ControlOrMeta+Alt+P");
    await expect(dialog.locator(".diff-viewer-comment-input")).toHaveValue("unfinished comment");
    await expect(modal.getByRole("region", { name: "Markdown preview" })).toHaveCount(0);
  });

  test("reopening a different file keeps the buffers and pending saves separate", async ({
    page,
    request,
  }) => {
    const dir = previewTempDir("shelley-preview-switch-");
    const first = join(dir, "first.md");
    const second = join(dir, "second.md");
    writeFileSync(first, "# First file\n");
    writeFileSync(second, "# Second file\n");
    const slug = await createConversationViaAPI(request, "Hello", { cwd: dir });
    await page.goto(`/c/${slug}`);
    const modal = await openFile(page, "first.md");
    await modal.locator(".monaco-editor textarea.inputarea").focus();
    await page.keyboard.press("ControlOrMeta+End");
    await page.keyboard.type("\n## Pending edit");
    await page.keyboard.press("ControlOrMeta+Alt+P");
    await expect(modal.getByRole("heading", { name: "Pending edit" })).toBeVisible();

    // The finder stacks above the editor. Selecting a second file must
    // unmount the first editor, flushing its pending save to the FIRST path.
    await page.keyboard.press("ControlOrMeta+P");
    const finder = page.locator(".grp-filter");
    await expect(finder).toBeVisible();
    await finder.fill("second.md");
    await expect(page.locator(".grp-row").first()).toContainText("second.md");
    await page.keyboard.press("ControlOrMeta+Alt+P");
    await expect(modal.getByRole("region", { name: "Markdown preview" })).toBeVisible();
    await finder.press("Enter");
    const nextModal = page.getByRole("dialog", { name: /Edit .*second\.md/ });
    await expect(nextModal.locator(".monaco-editor")).toBeVisible();
    await expect(nextModal.getByRole("region", { name: "Markdown preview" })).toHaveCount(0);
    await nextModal.getByRole("button", { name: "Preview mode" }).click();
    await expect(nextModal.getByRole("heading", { name: "Second file" })).toBeVisible();
    await expect(nextModal).not.toContainText("Pending edit");
    await expect.poll(() => readFileSync(first, "utf8")).toContain("Pending edit");
    expect(readFileSync(second, "utf8")).toBe("# Second file\n");
  });

  test("serializes saves when switching files during an in-flight write", async ({
    page,
    request,
  }) => {
    const dir = previewTempDir("shelley-preview-save-order-");
    const first = join(dir, "first.md");
    writeFileSync(first, "# First\n");
    writeFileSync(join(dir, "second.md"), "# Second\n");

    let releaseFirst!: () => void;
    const firstAllowed = new Promise<void>((resolve) => {
      releaseFirst = resolve;
    });
    let firstStarted!: () => void;
    const started = new Promise<void>((resolve) => {
      firstStarted = resolve;
    });
    let intercepted = false;
    await page.route("**/api/write-file", async (route) => {
      if (!intercepted && route.request().postDataJSON().path === first) {
        intercepted = true;
        firstStarted();
        await firstAllowed;
      }
      await route.continue();
    });

    try {
      const slug = await createConversationViaAPI(request, "Hello", { cwd: dir });
      await page.goto(`/c/${slug}`);
      const modal = await openFile(page, "first.md");
      await modal.locator(".monaco-editor textarea.inputarea").focus();
      await page.keyboard.press("ControlOrMeta+End");
      await page.keyboard.type("\n## First edit");
      await started; // the first auto-save is still held at the network boundary
      await page.keyboard.press("ControlOrMeta+End");
      await page.keyboard.type("\n## Latest edit");
      await page.keyboard.press("ControlOrMeta+P");
      const finder = page.locator(".grp-filter");
      await finder.fill("second.md");
      await expect(page.locator(".grp-row").first()).toContainText("second.md");
      await finder.press("Enter");
      await expect(
        page.getByRole("dialog", { name: /Edit .*second\.md/ }).locator(".monaco-editor"),
      ).toBeVisible();
      await page.keyboard.press("ControlOrMeta+P");
      const reopenFinder = page.locator(".grp-filter");
      await expect(reopenFinder).toBeVisible();
      await reopenFinder.fill("first.md");
      await expect(page.locator(".grp-row").first()).toContainText("first.md");
      await reopenFinder.press("Enter");
      const reopened = page.getByRole("dialog", { name: /Edit .*first\.md/ });
      await expect(reopened).toBeVisible();
      await expect(reopened.locator(".monaco-editor")).toHaveCount(0);
      releaseFirst();
      await expect(reopened.locator(".monaco-editor")).toBeVisible();
      await expect(reopened.locator(".view-lines")).toContainText("Latest edit");
      await expect.poll(() => readFileSync(first, "utf8")).toContain("Latest edit");
    } finally {
      releaseFirst();
    }
  });

  test("recovers unsaved edits when switching files after a failed save", async ({
    page,
    request,
  }) => {
    const dir = previewTempDir("shelley-preview-failed-save-");
    const first = join(dir, "first.md");
    writeFileSync(first, "# First\n");
    writeFileSync(join(dir, "second.md"), "# Second\n");
    let failWrites = true;
    await page.route("**/api/write-file", async (route) => {
      if (failWrites && route.request().postDataJSON().path === first) {
        await route.fulfill({ status: 503, body: "write failed" });
      } else {
        await route.continue();
      }
    });

    const slug = await createConversationViaAPI(request, "Hello", { cwd: dir });
    await page.goto(`/c/${slug}`);
    const modal = await openFile(page, "first.md");
    await modal.locator(".monaco-editor textarea.inputarea").focus();
    await page.keyboard.press("ControlOrMeta+End");
    await page.keyboard.type("\n## Keep me");
    await openFile(page, "second.md");
    const reopened = await openFile(page, "first.md");
    await expect(reopened.locator(".view-lines")).toContainText("Keep me");
    await expect(reopened.locator(".agents-md-save-error")).toBeVisible();
    expect(readFileSync(first, "utf8")).toBe("# First\n");

    failWrites = false;
    await reopened.locator(".monaco-editor textarea.inputarea").focus();
    await page.keyboard.press("ControlOrMeta+S");
    await expect.poll(() => readFileSync(first, "utf8")).toContain("Keep me");
  });
});

test.describe("Markdown preview on mobile", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("keeps wide tables inside the reading pane", async ({ page, request }) => {
    const dir = previewTempDir("shelley-mobile-preview-");
    writeFileSync(
      join(dir, "README.md"),
      "# Mobile notes\n\n| Key | Very long description |\n| --- | --- |\n| Value | " +
        "unbrokenvalue".repeat(20) +
        " |\n",
    );
    const slug = await createConversationViaAPI(request, "Hello", { cwd: dir });
    await page.goto(`/c/${slug}`);
    const modal = await openFile(page, "README.md");
    await expect(modal.getByRole("button", { name: "Split view" })).toHaveCount(0);
    await modal.getByRole("button", { name: "Preview mode" }).click();
    const preview = modal.getByRole("region", { name: "Markdown preview" });
    await expect(preview.getByRole("heading", { name: "Mobile notes" })).toBeVisible();
    await expect(preview.locator("table")).toBeVisible();
    const widths = await preview.evaluate((el) => ({
      pane: el.clientWidth,
      content: el.scrollWidth,
    }));
    expect(widths.content).toBeLessThanOrEqual(widths.pane);
    await modal.getByRole("button", { name: "Edit mode" }).click();
    await expect(modal.locator(".monaco-editor textarea.inputarea")).not.toBeFocused();
    await expect(modal.locator(".agents-md-header-path")).toBeVisible();
  });
});
