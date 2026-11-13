import { test, expect } from "@playwright/test";
import { openWorkspaceTool, createConversationViaAPI } from "./helpers";

// This test exercises the Ctrl+F find widget inside the diff viewer.
// It verifies that:
//   1. Ctrl+F opens Monaco's find widget (not the browser's)
//   2. Typing in the find widget works (keys like "." don't trigger navigation)
//   3. Escape closes the find widget without closing the diff viewer

test.describe("Diff viewer find widget", () => {
  test("Ctrl+F opens Monaco find, typing works, Escape closes find not viewer", async ({
    page,
    request,
  }) => {
    test.setTimeout(60000);

    // The test server runs inside the shelley repo, which is a git repo.
    // We need the shelley root dir. Fetch it from the git diffs API using
    // the default CWD (the server's working dir).
    const cwdResp = await request.get("/api/git/diffs?cwd=.");
    expect(cwdResp.ok()).toBeTruthy();
    const cwdData = await cwdResp.json();
    const gitRoot = cwdData.gitRoot;
    expect(gitRoot).toBeTruthy();

    const slug = await createConversationViaAPI(request, "Hello", { cwd: gitRoot });

    // Navigate to the conversation.
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    await openWorkspaceTool(page, "Diffs");

    // Wait for the diff viewer overlay to appear.
    const overlay = page.locator(".diff-viewer-overlay");
    await expect(overlay).toBeVisible({ timeout: 10000 });

    // Select the first non-empty commit if working changes are empty.
    // The diff viewer auto-selects, but we need a file to be loaded.
    if (page.viewportSize()!.width < 768) {
      const overview = overlay.locator(".diff-rethink-mobile-files.is-overview");
      if (!(await overview.isVisible())) await overlay.locator(".diff-rethink-file-jump").click();
      await overlay.locator(".diff-rethink-mobile-files .diff-tree-code-row[aria-selected]").first().click();
      await expect(overlay.locator(".diff-rethink-file-name")).toBeVisible();
    } else {
      // Desktop retains its native file selector.
      const fileSelect = overlay.locator("select.diff-viewer-select").first();
      await expect(async () => {
        const options = await fileSelect.locator("option").count();
        expect(options).toBeGreaterThan(1);
      }).toPass({ timeout: 15000 });
    }

    // Wait for Monaco editor to render inside the diff viewer.
    const editorContainer = overlay.locator(".diff-viewer-editor");
    await expect(async () => {
      const visible = await editorContainer.isVisible();
      expect(visible).toBeTruthy();
      // Monaco creates .monaco-editor elements when ready
      const monacoEl = await editorContainer.locator(".monaco-editor").count();
      expect(monacoEl).toBeGreaterThan(0);
    }).toPass({ timeout: 15000 });

    // Verify the find widget is NOT visible initially.
    const findWidget = editorContainer.locator(".find-widget.visible");
    await expect(findWidget).toHaveCount(0);

    // Press Ctrl+F to open the find widget.
    await page.keyboard.press("Control+f");

    // Wait for the find widget to become visible.
    await expect(findWidget).toBeVisible({ timeout: 5000 });

    // The find input should be focused. Type a search query that includes
    // "." — this character would normally trigger "next change" navigation.
    await page.keyboard.type("test.file", { delay: 50 });

    // Verify the text was typed into the find input (not swallowed by shortcuts).
    const findInput = findWidget.getByRole("textbox", { name: "Find" });
    await expect(findInput).toHaveValue(/test\.file/, { timeout: 5000 });

    // The diff viewer should still be open.
    await expect(overlay).toBeVisible();

    // Press Escape to close the find widget.
    await page.keyboard.press("Escape");

    // The find widget should be hidden now.
    await expect(findWidget).toHaveCount(0, { timeout: 5000 });

    // The diff viewer should still be open (Escape only closed the find widget).
    await expect(overlay).toBeVisible();

    // Now press Escape again to close the diff viewer.
    await page.keyboard.press("Escape");
    await expect(overlay).toHaveCount(0, { timeout: 5000 });
  });
});
