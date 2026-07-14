import { test, expect } from "@playwright/test";
import { fileURLToPath } from "node:url";
import path, { dirname } from "node:path";
import { mkdirSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { createConversationViaAPI } from "./helpers";

const e2eDir = dirname(fileURLToPath(import.meta.url));

// The base Modal.vue is backed by PrimeVue Dialog (focus trap,
// role="dialog"/aria-modal, mask + Escape owned by PrimeVue). See
// components/Modal.vue. The DOM/ARIA contract that the other specs rely on
// (.modal-overlay mask, .modal panel, .modal-header .btn-icon close,
// .modal-body) is preserved via the #container slot and the mask passthrough;
// here we exercise the PrimeVue-specific guarantees.
//
// The Feature flags modal is the simplest consumer of the base Modal, and is
// reachable via the command palette (Cmd/Ctrl+K -> "feature").
test.describe("Base modal (PrimeVue Dialog)", () => {
  test("opens via palette, traps focus, and closes via X / Escape / backdrop", async ({
    page,
    request,
  }) => {
    test.setTimeout(60000);

    const slug = await createConversationViaAPI(request, "Hello");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const openFeatureFlags = async () => {
      // Cmd/Ctrl+K opens the command palette; type to filter to "Feature flags".
      await page.keyboard.press("ControlOrMeta+k");
      const search = page.locator(".command-palette-input");
      await expect(search).toBeVisible();
      await search.fill("feature");
      const item = page.locator(".command-palette-item").first();
      await expect(item).toBeVisible();
      await item.click();
    };

    // --- Open + contract / a11y ---
    await openFeatureFlags();

    const mask = page.locator(".modal-overlay");
    const panel = page.locator(".modal");
    await expect(mask).toBeVisible();
    await expect(panel).toBeVisible();
    await expect(page.locator(".modal-title")).toHaveText("Feature flags");

    // PrimeVue Dialog gives us proper dialog semantics on the wrapper around
    // our .modal panel.
    const dialog = page.getByRole("dialog");
    await expect(dialog).toHaveAttribute("aria-modal", "true");

    // The dialog has an accessible name: aria-labelledby must resolve to the
    // real .modal-title (not PrimeVue's default, never-rendered _header id).
    const labelledBy = await dialog.getAttribute("aria-labelledby");
    expect(labelledBy).toBeTruthy();
    await expect(page.locator(`#${labelledBy}`)).toHaveText("Feature flags");
    await expect(page.locator(`#${labelledBy}`)).toHaveClass(/modal-title/);

    // Focus moves into the dialog on open (so the focus trap engages).
    await expect
      .poll(() =>
        page.evaluate(() => document.querySelector(".modal")?.contains(document.activeElement)),
      )
      .toBe(true);

    // The mask must carry the legacy .modal-overlay class (passthrough), and
    // the close button must keep its aria-label contract.
    await expect(page.locator(".modal-header .btn-icon[aria-label='Close modal']")).toBeVisible();

    // --- Close via the X button ---
    await page.locator(".modal-header .btn-icon[aria-label='Close modal']").click();
    await expect(panel).toHaveCount(0);

    // --- Close via Escape (PrimeVue document-level handler) ---
    await openFeatureFlags();
    await expect(panel).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(panel).toHaveCount(0);

    // --- Close via backdrop / dismissable mask click ---
    await openFeatureFlags();
    await expect(panel).toBeVisible();
    // Click the mask at a corner well outside the centered panel.
    await mask.click({ position: { x: 5, y: 5 } });
    await expect(panel).toHaveCount(0);
  });
});

// On desktop the centered panel leaves wide gaps left/right. The transparent
// p-dialog wrapper spans the mask there, and must not swallow backdrop clicks
// (pointer-events: none in styles.css): PrimeVue's dismissable-mask only
// fires when the click target IS the mask element itself. On the default
// mobile viewport the panel is near-full-width, so this can only regress on
// desktop — hence the viewport override.
test.describe("Base modal backdrop click beside the panel (desktop)", () => {
  test.use({ viewport: { width: 1280, height: 800 } });

  test("dismisses when clicking beside the panel at panel mid-height", async ({
    page,
    request,
  }) => {
    test.setTimeout(60000);

    const slug = await createConversationViaAPI(request, "Hello");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    await page.keyboard.press("ControlOrMeta+k");
    const search = page.locator(".command-palette-input");
    await expect(search).toBeVisible();
    await search.fill("feature");
    await page.locator(".command-palette-item").first().click();

    const panel = page.locator(".modal");
    await expect(panel).toBeVisible();
    const box = await panel.boundingBox();
    await page.mouse.click(box!.x / 2, box!.y + box!.height / 2);
    await expect(panel).toHaveCount(0);
  });
});

// DirectoryPickerModal is the most involved consumer of the shared Modal: it
// uses the #footer slot (New Folder / Cancel / Select) and has an inline
// create form whose local Escape must cancel create mode without closing the
// dialog (it stopPropagation()s before the shared modal Escape stack sees it).
test.describe("Directory picker modal (shared Modal consumer)", () => {
  test("opens from the cwd chip, pins the footer, and handles nested Escape", async ({ page }) => {
    test.setTimeout(60000);

    // The cwd chip only shows on the new-conversation status bar. Use /new
    // explicitly: "/" resumes the most recent conversation on a shared server.
    await page.goto("/new");
    await page.waitForLoadState("domcontentloaded");

    const chip = page.locator(".status-field-cwd .status-chip");
    await expect(chip).toBeVisible();
    await chip.click();

    const panel = page.locator(".modal.directory-picker-modal");
    await expect(panel).toBeVisible();
    await expect(page.locator(".modal-title")).toHaveText("Select Directory");
    await expect(page.getByRole("dialog")).toHaveAttribute("aria-modal", "true");

    // Other tests seed conversations with cwd=/tmp, which can be huge on a
    // busy machine (tens of thousands of entries) and makes every re-render
    // crawl. Point the picker at this small, known directory instead.
    await panel.locator(".directory-picker-input").fill(e2eDir + "/");
    await expect(panel.locator(".directory-picker-current-path")).toContainText("e2e");

    // Footer slot renders the action row inside the shared .modal-footer.
    const footer = panel.locator(".modal-footer");
    await expect(footer.getByRole("button", { name: "Select" })).toBeVisible();
    await expect(footer.getByRole("button", { name: "Cancel" })).toBeVisible();

    // Enter inline-create mode; Escape cancels it but keeps the dialog open.
    await footer.getByRole("button", { name: "New Folder" }).click();
    const createInput = panel.locator(".directory-picker-create-input");
    await expect(createInput).toBeVisible();
    await createInput.press("Escape");
    await expect(createInput).toHaveCount(0);
    await expect(panel).toBeVisible();

    // A second Escape (now routed to the modal Escape stack) closes it.
    await page.keyboard.press("Escape");
    await expect(panel).toHaveCount(0);

    // Reopen and close via the footer Cancel button.
    await chip.click();
    await expect(panel).toBeVisible();
    await footer.getByRole("button", { name: "Cancel" }).click();
    await expect(panel).toHaveCount(0);
  });

  test("caps rendering of huge directories and filters within them", async ({ page }) => {
    test.setTimeout(60000);

    // A directory with more subdirs than MAX_VISIBLE_ENTRIES (500). Rendering
    // all of them used to freeze the tab for seconds (e.g. /tmp on a busy
    // box); the picker must cap the list and show a "type to filter" hint.
    const bigDir = mkdtempSync(path.join(tmpdir(), "dirpicker-big-"));
    try {
      for (let i = 0; i < 520; i++) {
        mkdirSync(path.join(bigDir, `sub-${String(i).padStart(4, "0")}`));
      }
      mkdirSync(path.join(bigDir, "zz-needle"));

      await page.goto("/new");
      await page.waitForLoadState("domcontentloaded");
      await page.locator(".status-field-cwd .status-chip").click();

      const panel = page.locator(".modal.directory-picker-modal");
      await expect(panel).toBeVisible();
      await panel.locator(".directory-picker-input").fill(bigDir + "/");

      // 500 rows rendered (+1 for the ".." parent row), remainder in the hint.
      await expect(panel.locator(".directory-picker-entry")).toHaveCount(501);
      await expect(panel.locator(".directory-picker-truncated")).toContainText(
        "more — type to filter",
      );

      // Filtering reaches entries beyond the cap. Count entry names (the
      // ".." parent row has no .directory-picker-entry-name).
      await panel.locator(".directory-picker-input").fill(bigDir + "/zz");
      await expect(panel.locator(".directory-picker-entry-name")).toHaveCount(1);
      await expect(panel.locator(".directory-picker-entry-name")).toContainText("zz-needle");

      await page.keyboard.press("Escape");
      await expect(panel).toHaveCount(0);
    } finally {
      rmSync(bigDir, { recursive: true, force: true });
    }
  });
});
